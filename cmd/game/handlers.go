package main

// Sync HTTP handlers for single-game mutations. The 6-pod backend was
// originally event-sourced for everything; that turned out to be the
// wrong tier for user-initiated chess actions (the SPA expects each
// button to round-trip a new StateJSON synchronously). These handlers
// restore the monolith's REST contract while keeping the Streams layer
// for cross-service intent (matchmaking) and CPU-asymmetric workloads
// (engine search). See CLAUDE.md for the Streams-vs-HTTP rule.
//
// Every handler that mutates state follows the same pattern:
//   1. authedUserID(r)            — gateway injected after JWT validation
//   2. db.GetGame + userOwnsGame  — authz on the row
//   3. acquireGameLock            — per-game-id Redis token lock so two
//                                    replicas can't race the same game
//   4. game.Load + mutation       — rehydrate, validate, apply
//   5. db.SaveGame                — write-through to PG
//   6. bus.EmitEvent              — fan out new state via game.evt.{id}
//   7. maybeTriggerEngine         — dispatch a search if engine's turn
//   8. return snapshotFromRecord  — same wire shape /api/state returns

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/game"
)

// gameLockTTL bounds how long any single mutation can hold the lock.
// Generous enough for slow DB writes; small enough that a crashed
// holder doesn't strand the game.
const gameLockTTL = 10 * time.Second

// gameIDFrom extracts the game id from a request. Prefers the RESTful
// path-param form (/api/games/{id}/<verb>) and falls back to the legacy
// ?game_id= query string. Returns "" if neither is set. One helper so
// every per-game handler reads the id the same way regardless of which
// URL style its route uses.
func gameIDFrom(r *http.Request) string {
	if id := r.PathValue("id"); id != "" {
		return id
	}
	return r.URL.Query().Get("game_id")
}

// requireGameAccess loads the game, verifies the caller owns it, and
// returns (record, userID, nil) on success. Writes 4xx and returns nil
// on failure. Handlers use it as a single guard line; centralizing it
// keeps the authz behaviour uniform.
func (s *GameService) requireGameAccess(w http.ResponseWriter, r *http.Request) (*db.GameRecord, int64, bool) {
	uid, ok := authedUserID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, 0, false
	}
	gameID := gameIDFrom(r)
	if gameID == "" {
		http.Error(w, "missing game_id", http.StatusBadRequest)
		return nil, 0, false
	}
	rec, err := s.getGameCached(r.Context(), gameID)
	if err != nil {
		http.Error(w, "game not found", http.StatusNotFound)
		return nil, 0, false
	}
	if !userOwnsGame(uid, rec) {
		http.Error(w, "game not found", http.StatusNotFound) // don't leak existence
		return nil, 0, false
	}
	return rec, uid, true
}

// withLockedMutation is the boilerplate every mutation handler shares:
// acquire the per-game lock, refetch the row inside the lock, call fn,
// save, publish a state event, and write the snapshot. fn returns the
// mutated *game.Game; the helper handles the rest.
func (s *GameService) withLockedMutation(
	w http.ResponseWriter, r *http.Request,
	fn func(gm *game.Game, rec *db.GameRecord) error,
) {
	rec, _, ok := s.requireGameAccess(w, r)
	if !ok {
		return
	}
	lock, err := acquireGameLock(r.Context(), s.bus.Rdb(), rec.ID, gameLockTTL)
	if err != nil {
		http.Error(w, "lock acquire failed", http.StatusInternalServerError)
		return
	}
	if lock == nil {
		http.Error(w, "another writer is mutating this game; retry", http.StatusConflict)
		return
	}
	defer lock.release(context.Background())

	// Refetch INSIDE the lock so we see writes from the previous holder.
	rec, err = s.getGameCached(r.Context(), rec.ID)
	if err != nil {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}

	// Replay from StartFEN ("" = standard start), NOT rec.FEN. rec.FEN
	// is the *current* (post-move) position and the moves in history
	// are illegal there. Loading from StartFEN replays cleanly so
	// History/HistorySAN/UndoStack/LastMove are all correctly
	// populated, which is what makes Undo, the move list, and the
	// last-move highlight work. For board-editor games StartFEN is
	// the user-supplied setup; for everything else it's "".
	gm := game.NewGame()
	var history []string
	_ = json.Unmarshal([]byte(rec.History), &history)
	gm.Load(rec.StartFEN, history, rec.EngineWhite, rec.EngineBlack)

	historyBefore := len(gm.History)
	if err := fn(gm, rec); err != nil {
		// fn already wrote a response if its error is user-facing;
		// otherwise log and 500.
		var ue *userError
		if errors.As(err, &ue) {
			http.Error(w, ue.msg, ue.status)
			return
		}
		slog.Error("mutation handler failed", "game_id", rec.ID, "error", err)
		http.Error(w, "mutation failed", http.StatusInternalServerError)
		return
	}

	// Server-authoritative clock update. Only fires when fn extended
	// the move list by one (a real move) — set_players, undo, new all
	// either shrink or leave history alone and shouldn't tick the
	// clock. Engine games carry no clock; loadClock returns nil.
	clockTimedOut := false
	if len(gm.History) == historyBefore+1 {
		if c, _ := loadClock(r.Context(), s.bus.Rdb(), rec.ID); c != nil {
			flagged, loser := c.applyMove(time.Now().UnixMilli())
			if flagged {
				rec.Status = "timeout"
				if loser == "w" {
					rec.Result = "0-1"
				} else {
					rec.Result = "1-0"
				}
				clockTimedOut = true
				_ = c.save(r.Context(), s.bus.Rdb())
				deleteClock(r.Context(), s.bus.Rdb(), rec.ID)
			} else {
				_ = c.save(r.Context(), s.bus.Rdb())
				c.scheduleFlag(r.Context(), s.bus.Rdb())
			}
		}
	}

	// Write the new state back. Status is taken from gm.Status() so
	// terminal conditions (checkmate / stalemate / draw rules) land in
	// the row automatically.
	histJSON, _ := json.Marshal(gm.History)
	sanJSON, _ := json.Marshal(gm.HistorySAN)
	rec.FEN = gm.Board.FEN()
	rec.History = string(histJSON)
	rec.HistorySAN = string(sanJSON)
	rec.EngineWhite = gm.EngineWhite
	rec.EngineBlack = gm.EngineBlack
	if !clockTimedOut {
		rec.Status = string(gm.Status())
	}
	rec.UpdatedAt = time.Now()
	if err := s.saveGameCached(r.Context(), rec); err != nil {
		slog.Error("save game failed", "game_id", rec.ID, "error", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}

	// Emit a synthesized snapshot on the per-game channel so any other
	// pod's WS clients re-render. Same channel the consumer-loop
	// MakeMove emits — single source of truth for "state changed".
	snapshot := s.snapshotFromRecord(r.Context(), rec)
	snapPayload, _ := json.Marshal(snapshot)
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type:    "StateUpdated",
		GameID:  rec.ID,
		Payload: snapPayload,
	})

	// Any state change invalidates a previously-set "thinking" sentinel
	// — if a human just moved or reset the game, the old engine search
	// is no longer relevant. triggerEngineForMove below will re-set it
	// (with the right TTL) when applicable.
	_ = s.bus.Rdb().Del(context.Background(), "game:thinking:"+rec.ID).Err()

	// Position-derived terminal (checkmate / stalemate / draw rules)
	// also tears down the clock so the sweeper stops checking it.
	if rec.Status != "" && rec.Status != "ongoing" {
		deleteClock(r.Context(), s.bus.Rdb(), rec.ID)
	}

	// If it's now an engine's turn and the game isn't over, kick off
	// the search. Async on a background context so the HTTP response
	// returns immediately; the engine result arrives later via the
	// engine:results stream and lands as a MakeMove Command.
	if gm.Status() == game.StatusOngoing && gm.EngineToMove() {
		s.triggerEngineForMove(rec, gm)
	}

	writeJSON(w, snapshot)
}

// userError lets handler closures bail with a specific HTTP status
// without panicking through the writer twice.
type userError struct {
	status int
	msg    string
}

func (e *userError) Error() string { return e.msg }

func userErr(status int, msg string) error { return &userError{status: status, msg: msg} }

// triggerEngineForMove dispatches an EngineRequest and marks the game
// "thinking" in Redis. The TTL on the sentinel key bounds how long the
// SPA shows the spinner if the worker silently dies; the engine result
// path clears it via a Del when the move is applied.
func (s *GameService) triggerEngineForMove(rec *db.GameRecord, gm *game.Game) {
	moveTime := time.Duration(rec.WhiteThinkTime) * time.Millisecond
	if gm.Board.SideToMove == core.Black {
		moveTime = time.Duration(rec.BlackThinkTime) * time.Millisecond
	}
	if moveTime <= 0 {
		moveTime = 1000 * time.Millisecond
	}

	// 2× moveTime + 2s budget for transport + ack. If the engine
	// search legitimately takes that long, the sentinel re-expires
	// itself when the result lands.
	ttl := 2*moveTime + 2*time.Second
	// Bot-fallback games hold the spinner across a 3–10s humanizing
	// delay added in processEngineResult before the MakeMove dispatch
	// (see cmd/game/matchmaker.go: botReactionDelays). The default
	// TTL above would expire mid-delay and let the SPA drop the
	// spinner / unlock clicks. Bump to botSentinelTTL (15s) so we cover
	// max(delay) + safety margin.
	// TODO(matchmaker-engine-fallback): remove with the bot pool.
	if isBotMatch(rec) {
		ttl = botSentinelTTL
	}
	_ = s.bus.Rdb().Set(context.Background(), "game:thinking:"+rec.ID, "1", ttl).Err()

	req := eventbus.EngineRequest{
		GameID:   rec.ID,
		FEN:      gm.Board.FEN(),
		History:  game.CopyHistory(gm.HistoryHash()),
		MoveTime: moveTime,
		Context:  "move",
	}
	if _, err := s.bus.SendEngineRequest(context.Background(), req); err != nil {
		slog.Error("dispatch engine request failed", "game_id", rec.ID, "error", err)
		_ = s.bus.Rdb().Del(context.Background(), "game:thinking:"+rec.ID).Err()
	}
}

// ===== MOVE =====

func (s *GameService) handleHTTPMove(w http.ResponseWriter, r *http.Request) {
	uid, _ := authedUserID(r) // requireGameAccess upstream guarantees this exists
	s.withLockedMutation(w, r, func(gm *game.Game, rec *db.GameRecord) error {
		var req struct {
			Move string `json:"move"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return userErr(http.StatusBadRequest, "invalid body")
		}
		// Two distinct "game over" gates because they catch different
		// terminations: gm.Status() reflects the *position* (checkmate,
		// stalemate, 50-move) while rec.Status reflects player-action
		// terminations (resign, timeout, agreed draw) that don't change
		// the position. Without the rec check, a player could keep
		// sending moves after they (or their opponent) resigned —
		// rec.Status would be silently overwritten by gm.Status() on
		// write-back. Same gate the engine-stream path holds in
		// handleMakeMove; this brings the HTTP path into lockstep.
		if gm.Status() != game.StatusOngoing {
			return userErr(http.StatusConflict, "game is already finished")
		}
		if rec.Status != "" && rec.Status != "ongoing" {
			return userErr(http.StatusConflict, "game is already finished")
		}
		if gm.EngineToMove() {
			return userErr(http.StatusConflict, "it is the engine's turn")
		}
		// Side-to-move authz. requireGameAccess proves the caller is
		// SOME participant; this proves they're the side currently
		// allowed to move. Without it, the white player could send
		// a black move on the black player's turn and the server
		// would happily apply it.
		if gm.Board.SideToMove == core.White {
			if rec.WhiteUserID == nil || *rec.WhiteUserID != uid {
				return userErr(http.StatusConflict, "it is not your turn")
			}
		} else {
			if rec.BlackUserID == nil || *rec.BlackUserID != uid {
				return userErr(http.StatusConflict, "it is not your turn")
			}
		}
		m, err := gm.Board.ParseUCIMove(req.Move)
		if err != nil {
			return userErr(http.StatusBadRequest, "invalid move format")
		}
		matched, ok := game.MatchMove(gm.Board.GenerateLegalMoves(), m)
		if !ok {
			return userErr(http.StatusForbidden, "illegal move")
		}
		gm.PlayMove(matched)
		return nil
	})
}

// ===== RESIGN =====
//
// Resign is a terminal mutation that doesn't change the *position* —
// only the row's status/result fields. withLockedMutation derives
// status from gm.Status() (still "ongoing" — pieces haven't moved),
// so resign needs its own path that bypasses the gm-derived save.
// We still take the per-game lock + ownership check, then flip the
// row directly and broadcast a terminal snapshot.

func (s *GameService) handleHTTPResign(w http.ResponseWriter, r *http.Request) {
	rec, uid, ok := s.requireGameAccess(w, r)
	if !ok {
		return
	}
	lock, err := acquireGameLock(r.Context(), s.bus.Rdb(), rec.ID, gameLockTTL)
	if err != nil {
		http.Error(w, "lock acquire failed", http.StatusInternalServerError)
		return
	}
	if lock == nil {
		http.Error(w, "another writer is mutating this game; retry", http.StatusConflict)
		return
	}
	defer lock.release(context.Background())

	rec, err = s.getGameCached(r.Context(), rec.ID)
	if err != nil {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}
	if rec.Status != "ongoing" {
		http.Error(w, "game is already finished", http.StatusConflict)
		return
	}
	switch {
	case rec.WhiteUserID != nil && *rec.WhiteUserID == uid:
		rec.Result = "0-1"
	case rec.BlackUserID != nil && *rec.BlackUserID == uid:
		rec.Result = "1-0"
	default:
		http.Error(w, "resign requires a human-vs-human game", http.StatusBadRequest)
		return
	}
	rec.Status = "resign"
	rec.UpdatedAt = time.Now()
	if err := s.saveGameCached(r.Context(), rec); err != nil {
		slog.Error("resign save failed", "game_id", rec.ID, "error", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	deleteClock(r.Context(), s.bus.Rdb(), rec.ID)

	snapshot := s.snapshotFromRecord(r.Context(), rec)
	snapPayload, _ := json.Marshal(snapshot)
	// Two events: StateUpdated for the SPA to repaint, GameFinished for
	// the durable stream so rating-updater fires.
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type: eventbus.EvtStateUpdated, GameID: rec.ID, Payload: snapPayload,
	})
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type: eventbus.EvtGameFinished, GameID: rec.ID, Payload: snapPayload,
	})
	writeJSON(w, snapshot)
}

// ===== NEW (reset same row) =====

func (s *GameService) handleHTTPNew(w http.ResponseWriter, r *http.Request) {
	s.withLockedMutation(w, r, func(gm *game.Game, rec *db.GameRecord) error {
		var req struct {
			EngineWhite bool `json:"engine_white"`
			EngineBlack bool `json:"engine_black"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		// Lock-out for finished true-PvP rows. The legacy /api/new
		// path reset the row in place, which let either side restart
		// a finished PvP game without the opponent's consent and
		// scribbled over the persisted history. Direct callers to the
		// /api/rematch_offer flow instead (cmd/game/rematches.go),
		// which creates a fresh row and requires a mutual handshake.
		// Bot-match rows look like PvP via the SPA mask but stay on
		// the in-place reset because the bot is the consent surrogate
		// (auto-accepts via maybeScheduleBotRematchResponse).
		// Engine-only games (one user_id nil) and ongoing PvP keep
		// the legacy reset behaviour.
		if rec.Status != "ongoing" &&
			rec.WhiteUserID != nil && rec.BlackUserID != nil &&
			!isBotMatch(rec) {
			return userErr(http.StatusBadRequest, "finished PvP games can only restart via /api/rematch_offer")
		}

		// Bot-match rematch: the SPA can't tell this was a bot game
		// (engine flags are masked in snapshotFromRecord), so it sends
		// engine_white=false / engine_black=false. Honoring that would
		// strip the bot's engine flag, leaving the row in a "PvP with
		// a phantom user who can never move" state. Preserve the
		// original bot-side assignment instead — same opponent, same
		// colors, fresh board. The user gets an instant rematch with
		// the bot "accepting".
		// TODO(matchmaker-engine-fallback): remove with the bot pool.
		wasBot := isBotMatch(rec)
		gm.Reset()
		if wasBot {
			gm.EngineWhite = rec.EngineWhite
			gm.EngineBlack = rec.EngineBlack
		} else {
			gm.EngineWhite = req.EngineWhite
			gm.EngineBlack = req.EngineBlack
		}
		// Wipe and re-initialize the clock from the row's time_control.
		// withLockedMutation will overwrite rec.Status to "ongoing"
		// (gm.Status is reset), so the new clock starts fresh.
		deleteClock(r.Context(), s.bus.Rdb(), rec.ID)
		if err := initClock(r.Context(), s.bus.Rdb(), rec); err != nil {
			slog.Error("clock reinit failed", "game_id", rec.ID, "error", err)
		}
		// Reset the import flag — a fresh game played on this row id
		// should count toward stats again, regardless of whether the
		// previous use of the row was a load_pgn replay.
		rec.Imported = false
		return nil
	})
}

// ===== SET_PLAYERS =====
//
// Bypasses withLockedMutation deliberately. The shared mutation
// helper auto-fires triggerEngineForMove whenever it's an engine's
// turn — but a settings change isn't a position-advancing event. If
// the engine is already searching with the OLD think time, that
// auto-trigger fires a SECOND request with the new time; both
// results race back, the second's bestmove is illegal in the
// already-advanced position and gets dropped, and the user's setting
// change effectively didn't apply. So set_players persists settings
// only and lets the in-flight search complete; the new think time
// takes effect on the NEXT search after that.

func (s *GameService) handleHTTPSetPlayers(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := s.requireGameAccess(w, r)
	if !ok {
		return
	}
	var req struct {
		EngineWhite    bool `json:"engine_white"`
		EngineBlack    bool `json:"engine_black"`
		WhiteThinkTime int  `json:"white_think_time"`
		BlackThinkTime int  `json:"black_think_time"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	lock, err := acquireGameLock(r.Context(), s.bus.Rdb(), rec.ID, gameLockTTL)
	if err != nil {
		http.Error(w, "lock acquire failed", http.StatusInternalServerError)
		return
	}
	if lock == nil {
		http.Error(w, "another writer is mutating this game; retry", http.StatusConflict)
		return
	}
	defer lock.release(context.Background())

	rec, err = s.getGameCached(r.Context(), rec.ID)
	if err != nil {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}
	// Bot-match games render as PvP in the SPA (the SidePanel hides
	// engine settings when both user_ids are set). The toggles aren't
	// reachable via the standard UI, but a hand-crafted request could
	// still land here — refuse it so the user can't accidentally rebind
	// which side the engine drives mid-game.
	if isBotMatch(rec) {
		http.Error(w, "engine settings are locked for matched games", http.StatusBadRequest)
		return
	}
	wasEngineWhite := rec.EngineWhite
	wasEngineBlack := rec.EngineBlack
	rec.EngineWhite = req.EngineWhite
	rec.EngineBlack = req.EngineBlack
	if req.WhiteThinkTime > 0 {
		rec.WhiteThinkTime = req.WhiteThinkTime
	}
	if req.BlackThinkTime > 0 {
		rec.BlackThinkTime = req.BlackThinkTime
	}
	rec.UpdatedAt = time.Now()
	if err := s.saveGameCached(r.Context(), rec); err != nil {
		slog.Error("set_players save failed", "game_id", rec.ID, "error", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}

	// Trigger BEFORE snapshot so the snapshot's `thinking` flag reflects
	// the just-set sentinel. Previously this ran after the snapshot, so
	// the SPA saw {engine_to_move: true, thinking: false} for one tick
	// and the spinner never appeared. Side effect: the WS pub/sub event
	// + the HTTP response now both carry the same post-trigger Rev, so
	// a fast engine reply still produces a strictly-higher Rev for the
	// next snapshot.
	//
	// Two reasons to re-evaluate the engine trigger here, not just on
	// assignment change:
	//   1. Think-time-only change with engine idle + engine-to-move
	//      (e.g. user opened a fresh EvE row and bumped think time
	//      before any move landed) — without a fresh trigger, the
	//      engine never starts and the game looks stuck.
	//   2. Assignment flip mid-search — the in-flight search will be
	//      dropped by the UserID==0 late-reply guard in handleMakeMove,
	//      but the `thinking` sentinel it set is still up, which would
	//      otherwise block the new side's trigger. Clear it first.
	engineAssignmentChanged := wasEngineWhite != rec.EngineWhite || wasEngineBlack != rec.EngineBlack
	if rec.Status == "ongoing" {
		if engineAssignmentChanged {
			_ = s.bus.Rdb().Del(r.Context(), "game:thinking:"+rec.ID).Err()
		}
		gm := game.NewGame()
		var history []string
		_ = json.Unmarshal([]byte(rec.History), &history)
		gm.Load(rec.StartFEN, history, rec.EngineWhite, rec.EngineBlack)
		if gm.EngineToMove() {
			// Skip if a search is already in flight (assignment unchanged
			// case where the engine is mid-think — let it finish with the
			// old think-time; the next move will pick up the new value).
			if v, _ := s.bus.Rdb().Get(r.Context(), "game:thinking:"+rec.ID).Result(); v != "1" {
				s.triggerEngineForMove(rec, gm)
			}
		}
	}

	snapshot := s.snapshotFromRecord(r.Context(), rec)
	snapPayload, _ := json.Marshal(snapshot)
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type: eventbus.EvtStateUpdated, GameID: rec.ID, Payload: snapPayload,
	})

	writeJSON(w, snapshot)
}

// ===== UNDO =====

func (s *GameService) handleHTTPUndo(w http.ResponseWriter, r *http.Request) {
	s.withLockedMutation(w, r, func(gm *game.Game, rec *db.GameRecord) error {
		// Unilateral undo is only allowed when there's no real opponent
		// (either side is the engine). For PvP, the agreed-upon
		// equivalent is the Takeback flow — see cmd/game/takebacks.go.
		if rec.WhiteUserID != nil && rec.BlackUserID != nil {
			return userErr(http.StatusBadRequest, "PvP games can't undo unilaterally; use Takeback instead")
		}
		gm.Undo()
		return nil
	})
}

// ===== DELETE =====

func (s *GameService) handleHTTPDelete(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := s.requireGameAccess(w, r)
	if !ok {
		return
	}
	// No lock: DeleteGame is a single PK row delete, and concurrent
	// deletes converge on rows-affected=0 which we treat as success.
	rows, err := s.db.DeleteGame(rec.ID)
	if err != nil {
		slog.Error("delete game failed", "game_id", rec.ID, "error", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	// Drop the cache entry whether or not rows>0; if the row was already
	// gone the cache is also stale, and either way we want a fresh PG
	// fallback on the next read.
	s.invalidateGameCache(r.Context(), rec.ID)
	if rows == 0 {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ===== HINT (async) =====

func (s *GameService) handleHTTPHint(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := s.requireGameAccess(w, r)
	if !ok {
		return
	}
	var req struct {
		MoveTime int `json:"movetime"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	moveTime := time.Duration(req.MoveTime) * time.Millisecond
	if moveTime <= 0 {
		moveTime = 1000 * time.Millisecond
	}

	var history []string
	_ = json.Unmarshal([]byte(rec.History), &history)
	gm := game.NewGame()
	gm.Load(rec.StartFEN, history, rec.EngineWhite, rec.EngineBlack)

	er := eventbus.EngineRequest{
		GameID:   rec.ID,
		FEN:      gm.Board.FEN(),
		History:  game.CopyHistory(gm.HistoryHash()),
		MoveTime: moveTime,
		Context:  "hint",
	}
	// Tag the hint with the requester so the result fans out only to
	// that user's private channel — not game.evt.{id}, which the
	// opponent in a PvP game also subscribes to. Without this, asking
	// for a hint in a rated game shows it on the opponent's board too.
	if uid, hasUID := authedUserID(r); hasUID {
		er.Metadata = map[string]string{"requester_id": strconv.FormatInt(uid, 10)}
	}
	if _, err := s.bus.SendEngineRequest(r.Context(), er); err != nil {
		http.Error(w, "dispatch failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
