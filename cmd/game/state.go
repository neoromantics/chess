package main

// Read-side state surface + visibility flip:
//
//   stateJSON / moveJSON  → wire contract the SPA expects from /api/state
//   handleState           → GET /api/games/{id}/state
//   maybeLazyEngineRetrigger → recovery for stuck engine searches
//   handleCanWatch        → cheap WS-upgrade preflight
//   handleSetVisibility   → POST /api/games/{id}/visibility
//   snapshotFromRecord    → the shared synthesizer used by every mutation
//                            handler's response and every event publish
//   handleReplayData      → GET /api/games/{id}/replay (per-ply frames)
//   handleListGames       → GET /api/games (cursor-paginated history)
//   writeJSON             → shared JSON-response helper used package-wide
//
// Pulled out of main.go to keep that file boot-only.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/game"
)

// stateJSON is the contract the SPA expects from /api/state. The DB
// row stores history / history_san / assessments as JSON-encoded
// strings (so a single Postgres column can hold a JSON array); the
// frontend expects parsed arrays plus derived fields (turn, legal
// moves, in_check, last_move). We synthesize the full snapshot here.
//
// Keep this in sync with frontend/src/types.ts:StateJSON. Renaming a
// field is a wire-protocol break; adding a field is safe.
type stateJSON struct {
	// ID is set on temp-game snapshots so the landing endpoint can
	// return the snapshot directly and the SPA learns its game ID
	// without a second request. Durable-game snapshots leave it
	// empty (the SPA already knows the ID — it's in the URL).
	ID   string `json:"id,omitempty"`
	FEN  string `json:"fen"`
	Turn string `json:"turn"`
	// Rev is a monotonic version stamp the SPA uses to reject stale
	// snapshots. Set to rec.UpdatedAt.UnixNano() — every persisted
	// mutation bumps UpdatedAt and the per-game lock serializes writes,
	// so the value is strictly increasing per row. Same snapshot arriving
	// twice (HTTP response + WS pub/sub) has the same Rev → the second
	// no-ops on the client. A newer snapshot via WS that races an older
	// one via HTTP wins instead of being overwritten by the slower
	// channel, which was the "engine moves then board reverts" bug.
	// Zero is treated as "unrevved" on the client (legacy snapshots).
	Rev            int64     `json:"rev,omitempty"`
	EngineWhite    bool      `json:"engine_white"`
	EngineBlack    bool      `json:"engine_black"`
	EngineToMove   bool      `json:"engine_to_move"`
	Status         string    `json:"status"`
	Result         string    `json:"result"`
	InCheck        bool      `json:"in_check"`
	LegalMoves     []string  `json:"legal_moves"`
	History        []string  `json:"history"`
	HistorySAN     []string  `json:"history_san"`
	LastMove       *moveJSON `json:"last_move"`
	Thinking       bool      `json:"thinking"`
	WhiteThinkTime int       `json:"white_think_time"`
	BlackThinkTime int       `json:"black_think_time"`

	// Player metadata so the SPA can decide which color the caller is,
	// flip the board for the black player, and show "you vs opponent".
	// nil pointers mean that side is an engine, not a human.
	WhiteUserID *int64 `json:"white_user_id"`
	BlackUserID *int64 `json:"black_user_id"`
	TimeControl string `json:"time_control"`
	Rated       bool   `json:"rated"`
	// Spectator-mode opt-in. True means /watch/{id} works and any
	// signed-in user can read the snapshot. The SPA mirrors this in
	// the SidePanel toggle (owner-only) and uses it to enter
	// read-only mode when the caller isn't a participant.
	IsPublic bool `json:"is_public"`

	// Imported is TRUE when the row's state was rewritten via
	// /api/load_pgn. Surfaced to the SPA so past-games lists can
	// render an "Imported — not counted" badge. Backend's
	// CountUserGameStats already excludes these.
	Imported bool `json:"imported,omitempty"`

	// Persisted per-ply move-assessment verdicts. Only populated when
	// the count matches the move-list length — a mismatched count
	// means the persisted assessments are stale (e.g. the user took a
	// move back since the last /api/analyze run) and shouldn't be
	// shown. Empty/missing on never-analyzed games. See cmd/game/analysis.go.
	Assessments []PlyAssessment `json:"assessments,omitempty"`

	// Server-authoritative clock state. Engine-only / pre-clocks games
	// leave ClockInitial=0; the SPA hides the clock UI when initial is 0.
	// WhiteClockMS/BlackClockMS are reduced for the mover at snapshot
	// time — the SPA extrapolates further locally for smoothness.
	WhiteClockMS  int64  `json:"white_clock_ms"`
	BlackClockMS  int64  `json:"black_clock_ms"`
	ClockInitial  int64  `json:"clock_initial_ms"`
	ClockInc      int64  `json:"clock_inc_ms"`
	ClockMover    string `json:"clock_mover"`     // "w", "b", or "" if not running
	ClockServerMS int64  `json:"clock_server_ms"` // unix ms at snapshot time, for SPA extrapolation
}

type moveJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (s *GameService) handleState(w http.ResponseWriter, r *http.Request) {
	gameID := gameIDFrom(r)
	if gameID == "" {
		http.Error(w, "missing game_id", 400)
		return
	}
	rec, err := s.getGameCached(r.Context(), gameID)
	if err != nil {
		http.Error(w, "game not found", 404)
		return
	}
	// Per-game read authorization. Spectator-aware: public games are
	// readable by anyone; private games are participant-only. Return
	// 404 (not 403) so existence doesn't leak.
	uid, _ := authedUserID(r)
	if !userMayRead(uid, rec) {
		http.Error(w, "game not found", 404)
		return
	}
	// Lazy engine retrigger. If the row says it's the engine's turn but
	// no search is in flight (sentinel cleared), the previous attempt
	// either lost its reply (dispatch failure cleared the sentinel) or
	// timed out (TTL expired with the worker dying mid-search). Either
	// way the engine will sit forever without external nudging. The
	// next /api/state poll — which the SPA does on WS reconnect — is
	// the natural place to recover: trigger a fresh search, return the
	// snapshot with `thinking=true` so the spinner re-appears, and let
	// the result land via the normal pipeline.
	s.maybeLazyEngineRetrigger(r.Context(), rec)
	writeJSON(w, s.snapshotFromRecord(r.Context(), rec))
}

// maybeLazyEngineRetrigger kicks off a fresh engine search if (a) the
// game is ongoing, (b) it's the engine's turn, and (c) no thinking
// sentinel is set. Idempotent: triggerEngineForMove SETs the sentinel
// so a second concurrent /api/state read won't double-dispatch.
// Skipped for bot-fallback rows during the bot's "reaction delay" —
// processEngineResult's goroutine holds the sentinel across that
// window, so the sentinel check already gates correctly.
func (s *GameService) maybeLazyEngineRetrigger(ctx context.Context, rec *db.GameRecord) {
	if rec == nil || rec.Status != "ongoing" {
		return
	}
	if !rec.EngineWhite && !rec.EngineBlack {
		return
	}
	gm := game.NewGame()
	var history []string
	_ = json.Unmarshal([]byte(rec.History), &history)
	gm.Load(rec.StartFEN, history, rec.EngineWhite, rec.EngineBlack)
	if !gm.EngineToMove() {
		return
	}
	v, err := s.bus.Rdb().Get(ctx, "game:thinking:"+rec.ID).Result()
	if err == nil && v == "1" {
		return // search already in flight
	}
	slog.Info("lazy engine retrigger", "game_id", rec.ID, "side", gm.Board.SideToMove)
	s.triggerEngineForMove(rec, gm)
}

// handleCanWatch is the cheap WS upgrade preflight: it returns 200 if
// the caller may read the game (participant on private, anyone on
// public). Skips the full snapshot synthesis that /api/state does.
// Used by the gateway's WS upgrade gate before promoting an HTTP
// connection — at scale we burn enough CPU on WS opens to matter.
func (s *GameService) handleCanWatch(w http.ResponseWriter, r *http.Request) {
	gameID := gameIDFrom(r)
	if gameID == "" {
		http.Error(w, "missing game_id", 400)
		return
	}
	rec, err := s.getGameCached(r.Context(), gameID)
	if err != nil {
		http.Error(w, "game not found", 404)
		return
	}
	uid, _ := authedUserID(r)
	if !userMayRead(uid, rec) {
		http.Error(w, "game not found", 404)
		return
	}
	// Surface participant-vs-spectator to the gateway so the hub can
	// exclude players from the viewer count without a second round-trip.
	if userOwnsGame(uid, rec) {
		w.Header().Set("X-Is-Player", "1")
	} else {
		w.Header().Set("X-Is-Player", "0")
	}
	w.WriteHeader(http.StatusOK)
}

// handleSetVisibility flips the spectator-mode flag on a game.
// Participant-only — userOwnsGame guards both the read and the write
// so a non-owner can neither discover the flag's current value nor
// flip it. Body: {"is_public": true|false}. Returns the updated row's
// is_public for the SPA to reflect immediately.
func (s *GameService) handleSetVisibility(w http.ResponseWriter, r *http.Request) {
	gameID := gameIDFrom(r)
	if gameID == "" {
		http.Error(w, "missing game_id", 400)
		return
	}
	uid, ok := authedUserID(r)
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	rec, err := s.getGameCached(r.Context(), gameID)
	if err != nil {
		http.Error(w, "game not found", 404)
		return
	}
	if !userOwnsGame(uid, rec) {
		// 404 (not 403) for the same existence-leak reason as everywhere else.
		http.Error(w, "game not found", 404)
		return
	}

	var body struct {
		IsPublic bool `json:"is_public"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", 400)
		return
	}

	// Lock for the read-modify-write so a concurrent SaveGame doesn't
	// overwrite this with a stale is_public.
	lock, lockErr := acquireGameLock(r.Context(), s.bus.Rdb(), gameID, gameLockTTL)
	if lockErr != nil || lock == nil {
		http.Error(w, "game busy", http.StatusConflict)
		return
	}
	defer lock.release(context.Background())

	rec, err = s.getGameCached(r.Context(), gameID)
	if err != nil {
		http.Error(w, "game not found", 404)
		return
	}
	rec.IsPublic = body.IsPublic
	rec.UpdatedAt = time.Now()
	if err := s.saveGameCached(r.Context(), rec); err != nil {
		http.Error(w, "save failed", 500)
		return
	}
	writeJSON(w, map[string]bool{"is_public": rec.IsPublic})
}

// snapshotFromRecord builds the wire-protocol StateJSON the SPA
// expects from a persisted GameRecord. The DB row's history /
// history_san / assessments columns hold JSON-encoded strings (one
// PG column per JSON array); we parse them, then rehydrate the game
// via game.Load so we can compute derived fields (turn, legal moves,
// last move, check status).
//
// thinking flag is sourced from a Redis sentinel key set by the engine
// trigger path; absent → false.
func (s *GameService) snapshotFromRecord(ctx context.Context, rec *db.GameRecord) stateJSON {
	var history, historySAN []string
	if rec.History != "" {
		_ = json.Unmarshal([]byte(rec.History), &history)
	}
	if rec.HistorySAN != "" {
		_ = json.Unmarshal([]byte(rec.HistorySAN), &historySAN)
	}
	if history == nil {
		history = []string{}
	}
	if historySAN == nil {
		historySAN = []string{}
	}

	// Replay from rec.StartFEN ("" = standard start), NOT rec.FEN.
	// rec.FEN is the *current* (post-move) position, and the moves in
	// history are illegal there (replay bombs out on move 1). Loading
	// from StartFEN replays cleanly so gm.History, gm.HistorySAN,
	// gm.UndoStack, and gm.LastMove are all correctly populated. This
	// is what makes undo work, the move list survive across mutations,
	// and the SAN column stay in sync with history. For board-editor
	// games StartFEN is the user-supplied setup; otherwise it's "".
	gm := game.NewGame()
	gm.Load(rec.StartFEN, history, rec.EngineWhite, rec.EngineBlack)
	_ = historySAN // SAN is recomputed by gm.Load; the saved column is just a wire shortcut

	turn := "w"
	if gm.Board.SideToMove == core.Black {
		turn = "b"
	}
	legal := gm.Board.GenerateLegalMoves()
	legalStrs := make([]string, len(legal))
	for i, m := range legal {
		legalStrs[i] = m.String()
	}
	var last *moveJSON
	if gm.LastMove != nil {
		last = &moveJSON{
			From: core.SquareName(gm.LastMove.From),
			To:   core.SquareName(gm.LastMove.To),
		}
	}

	thinking := false
	if v, err := s.bus.Rdb().Get(ctx, "game:thinking:"+rec.ID).Result(); err == nil && v == "1" {
		thinking = true
	}

	// rec.Status wins over gm.Status() whenever it's terminal. resign /
	// timeout / future agreed-draw don't change the *position* (gm.Status
	// would still report "ongoing"), but the row carries the real outcome.
	// Without this, resign silently has no effect — the SPA gets back
	// status="ongoing" on the very next snapshot and the user keeps
	// playing.
	status := string(gm.Status())
	if rec.Status != "" && rec.Status != "ongoing" {
		status = rec.Status
	}

	// Bot-match disguise: when this game was created by the engine-
	// fallback matchmaker (one side has BOTH a user_id AND an engine
	// flag set), report the engine flags as false to the SPA so it
	// renders the game as PvP — same gating, no "Engine settings"
	// disclosure, no "(engine thinking…)" tell. The server-side
	// gm.EngineToMove() / triggerEngine path still uses the truthful
	// rec.EngineWhite/EngineBlack so the engine actually moves.
	// TODO(matchmaker-engine-fallback): remove with the bot pool.
	engineWhiteOut := gm.EngineWhite
	engineBlackOut := gm.EngineBlack
	engineToMoveOut := gm.EngineToMove()
	if isBotMatch(rec) {
		engineWhiteOut = false
		engineBlackOut = false
		engineToMoveOut = false
	}

	// Hydrate persisted assessments only when they match the current
	// move list. A mismatch means the row mutated since the last
	// /api/analyze (a move, an undo, a takeback…) and the stored
	// verdicts no longer point at the right plies. Dropping them here
	// keeps the SPA from rendering nonsense markers; a fresh
	// /api/analyze regenerates and overwrites.
	var assessmentsOut []PlyAssessment
	if rec.Assessments != "" && rec.Assessments != "[]" {
		var parsed []PlyAssessment
		if err := json.Unmarshal([]byte(rec.Assessments), &parsed); err == nil &&
			len(parsed) == len(gm.History) {
			assessmentsOut = parsed
		}
	}

	snap := stateJSON{
		FEN:            gm.Board.FEN(),
		Turn:           turn,
		Rev:            rec.UpdatedAt.UnixNano(),
		EngineWhite:    engineWhiteOut,
		EngineBlack:    engineBlackOut,
		EngineToMove:   engineToMoveOut,
		Status:         status,
		Result:         rec.Result,
		InCheck:        gm.Board.InCheck(gm.Board.SideToMove),
		LegalMoves:     legalStrs,
		History:        gm.History,
		HistorySAN:     gm.HistorySAN,
		LastMove:       last,
		Thinking:       thinking,
		WhiteThinkTime: rec.WhiteThinkTime,
		BlackThinkTime: rec.BlackThinkTime,
		WhiteUserID:    rec.WhiteUserID,
		BlackUserID:    rec.BlackUserID,
		TimeControl:    rec.TimeControl,
		Rated:          rec.Rated,
		IsPublic:       rec.IsPublic,
		Imported:       rec.Imported,
		Assessments:    assessmentsOut,
	}

	// Clock projection: read the live hash, deduct mid-turn elapsed for
	// the mover, surface ClockServerMS so the SPA's local tick lines up.
	// Errors here are non-fatal — clock UI just stays hidden / stale.
	if c, err := loadClock(ctx, s.bus.Rdb(), rec.ID); err == nil && c != nil {
		w, b := c.currentTimes()
		snap.WhiteClockMS = w
		snap.BlackClockMS = b
		snap.ClockInitial = c.InitialMS
		snap.ClockInc = c.IncMS
		// Stop the clock visually once the game ends — flagGame zeros
		// Mover, but resign / checkmate finalize the row without
		// touching the clock hash. Rely on rec.Status to decide.
		if rec.Status != "" && rec.Status != "ongoing" {
			snap.ClockMover = ""
		} else {
			snap.ClockMover = c.Mover
		}
		snap.ClockServerMS = time.Now().UnixMilli()
	}
	return snap
}

// handleReplayData returns the per-ply ReplayFrame slice for a single
// game so the gateway's replay.html template substitution has something
// to embed. Pure JSON; gateway is responsible for wrapping it in HTML.
func (s *GameService) handleReplayData(w http.ResponseWriter, r *http.Request) {
	gameID := gameIDFrom(r)
	if gameID == "" {
		http.Error(w, "missing game_id", 400)
		return
	}
	rec, err := s.getGameCached(r.Context(), gameID)
	if err != nil {
		http.Error(w, "game not found", 404)
		return
	}
	// Mirror /api/state's userMayRead policy: public games are readable
	// by anyone (anonymous viewers included), private games only by
	// participants. The previous "only check when uid is present" branch
	// gave anonymous callers full access — and the gateway's
	// handleReplay never forwarded identity, so that branch was the
	// default path even for the owner. Use 404 (not 403) so missing
	// game vs. private game stay indistinguishable.
	uid, _ := authedUserID(r)
	if !userMayRead(uid, rec) {
		http.Error(w, "game not found", 404)
		return
	}
	var history []string
	if rec.History != "" {
		_ = json.Unmarshal([]byte(rec.History), &history)
	}
	gm := game.NewGame()
	gm.Load(rec.StartFEN, history, rec.EngineWhite, rec.EngineBlack)
	writeJSON(w, gm.ReplayData())
}

// handleListGames returns one cursor page of the user's games. The SPA
// requests the first page with no `before` param (server interprets as
// now); subsequent pages pass `before=<rfc3339 of last UpdatedAt>`.
// `limit` is capped server-side; the response shape stays a JSON array
// — the next-page cursor is just "the UpdatedAt of the last row" so
// the client doesn't need a wrapper object.
func (s *GameService) handleListGames(w http.ResponseWriter, r *http.Request) {
	// Identity comes from the gateway-injected X-User-ID header (set by
	// injectAuthedUser after JWT validation). The legacy ?user_id= query
	// param was removed in 3dba1d8; this handler was the lone holdout
	// from the 32f4b02 migration and silently returned an empty list
	// (via a 400 the SPA's loadGames swallows) for every signed-in user.
	userID, ok := authedUserID(r)
	if !ok {
		http.Error(w, "missing user_id", http.StatusBadRequest)
		return
	}

	cursor := time.Now()
	if v := r.URL.Query().Get("before"); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			cursor = t
		}
	}
	limit := 0 // 0 -> store-side default
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	games, err := s.db.ListGames(userID, cursor, limit)
	if err != nil {
		http.Error(w, "failed to list games", 500)
		return
	}
	writeJSON(w, games)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
