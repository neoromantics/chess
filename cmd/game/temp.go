package main

// Temporary, anonymous game sessions. The model is "land on the URL,
// get a game; come back within 10 minutes, your game is still there;
// otherwise it's gone forever." No Postgres row, no rating, no invites.
//
// Storage layout:
//   tempgame:state:{game_id}    JSON-encoded tempGameRec, EXPIRE 600s
//   tempgame:session:{anon_id}  string game_id (one game per browser),
//                               EXPIRE 600s
//
// Sliding TTL: every read AND write touches both keys via EXPIRE so the
// window only closes after 10 minutes of true inactivity. Redis is the
// only store — there is no PG sweeper to write, no migration to babysit.
//
// Identity: a chess-anon HttpOnly cookie carries an opaque UUID set by
// the gateway. The gateway injects ?anon_id=<uuid> on every /api/temp/*
// request. game-service trusts the injection (mirroring the user_id
// pattern) and uses anon_id == rec.OwnerAnonID as the sole authz check.
//
// Engine pipeline: SendEngineRequest carries Metadata{"temp":"1",
// "anon_id":"..."}; engine-worker echoes Metadata back; processEngineResult
// in main.go branches on Metadata["temp"] to call applyTempEngineMove
// instead of dispatching CmdMakeMove. Hub fan-out is the same:
// tempSnapshot publishes on game.evt.{id} which the existing
// PSUBSCRIBE game.evt.* picks up.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/game"
	"github.com/redis/go-redis/v9"
)

const (
	tempGameTTL    = 10 * time.Minute
	tempStateKey   = "tempgame:state:"
	tempSessionKey = "tempgame:session:"
)

// tempGameRec is the full ephemeral game record. Marshalled as a single
// JSON blob into one Redis key — there's no read-modify-write of
// individual fields anywhere, so the hash-of-fields layout the durable
// store uses would be over-engineered here.
type tempGameRec struct {
	ID          string   `json:"id"`
	OwnerAnonID string   `json:"owner_anon_id"`
	History     []string `json:"history"`
	EngineWhite bool     `json:"engine_white"`
	EngineBlack bool     `json:"engine_black"`
	// Split per-side so engine-vs-engine temp games can use asymmetric
	// think times, and so mid-game changes via /api/temp/set_players
	// land on the right side. ThinkTimeMS stays in the JSON tag set as
	// a fallback for in-flight Redis records written before the split.
	WhiteThinkTimeMS int    `json:"white_think_time_ms"`
	BlackThinkTimeMS int    `json:"black_think_time_ms"`
	ThinkTimeMS      int    `json:"think_time_ms,omitempty"` // legacy fallback
	Status           string `json:"status"`                  // "ongoing" | "checkmate" | ... | "resign"
	Result           string `json:"result"`                  // "*" | "1-0" | "0-1" | "1/2-1/2"
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
}

// thinkForSide returns the engine think duration to use when the named
// side is about to search. Reads the split fields and falls back to the
// legacy single ThinkTimeMS so records written before the split still
// work for the rest of their TTL window.
func (r *tempGameRec) thinkForSide(side core.Color) time.Duration {
	ms := r.WhiteThinkTimeMS
	if side == core.Black {
		ms = r.BlackThinkTimeMS
	}
	if ms <= 0 {
		ms = r.ThinkTimeMS
	}
	if ms <= 0 {
		ms = 1000
	}
	return time.Duration(ms) * time.Millisecond
}

type tempStore struct {
	rdb *redis.Client
}

func newTempStore(rdb *redis.Client) *tempStore { return &tempStore{rdb: rdb} }

func (t *tempStore) get(ctx context.Context, id string) (*tempGameRec, error) {
	v, err := t.rdb.Get(ctx, tempStateKey+id).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec tempGameRec
	if err := json.Unmarshal([]byte(v), &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func (t *tempStore) save(ctx context.Context, rec *tempGameRec) error {
	rec.UpdatedAt = time.Now().UnixMilli()
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return t.rdb.Set(ctx, tempStateKey+rec.ID, b, tempGameTTL).Err()
}

func (t *tempStore) sessionGameID(ctx context.Context, anonID string) (string, error) {
	v, err := t.rdb.Get(ctx, tempSessionKey+anonID).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return v, err
}

func (t *tempStore) setSession(ctx context.Context, anonID, gameID string) error {
	return t.rdb.Set(ctx, tempSessionKey+anonID, gameID, tempGameTTL).Err()
}

// refreshTTL extends both the state and session keys back to the full
// window. Called on every read/write. Errors are logged-and-ignored:
// a missed refresh just means the key may expire one cycle earlier
// than intended, which is acceptable for an ephemeral surface.
func (t *tempStore) refreshTTL(ctx context.Context, gameID, anonID string) {
	if gameID != "" {
		_ = t.rdb.Expire(ctx, tempStateKey+gameID, tempGameTTL).Err()
	}
	if anonID != "" {
		_ = t.rdb.Expire(ctx, tempSessionKey+anonID, tempGameTTL).Err()
	}
}

// requireTempAccess validates anon_id+game_id, loads the game, refreshes
// the TTL, and returns (rec, store, ok). On any failure writes a 4xx
// and returns ok=false — handlers stop on a single line.
func (s *GameService) requireTempAccess(w http.ResponseWriter, r *http.Request) (*tempGameRec, *tempStore, bool) {
	anonID := r.URL.Query().Get("anon_id")
	gameID := r.URL.Query().Get("game_id")
	if anonID == "" {
		http.Error(w, "missing anon_id", http.StatusBadRequest)
		return nil, nil, false
	}
	if gameID == "" {
		http.Error(w, "missing game_id", http.StatusBadRequest)
		return nil, nil, false
	}
	store := newTempStore(s.bus.Rdb())
	rec, err := store.get(r.Context(), gameID)
	if err != nil {
		slog.Error("temp store get failed", "game_id", gameID, "error", err)
		http.Error(w, "temp store error", http.StatusInternalServerError)
		return nil, nil, false
	}
	if rec == nil {
		// Don't distinguish "expired" from "never existed" — both are
		// "your URL no longer maps to a live game; ask for a new one."
		http.Error(w, "temp game not found or expired", http.StatusNotFound)
		return nil, nil, false
	}
	if rec.OwnerAnonID != anonID {
		// Same 404 as missing — don't leak that someone else's game ID
		// even exists.
		http.Error(w, "temp game not found or expired", http.StatusNotFound)
		return nil, nil, false
	}
	store.refreshTTL(r.Context(), gameID, anonID)
	return rec, store, true
}

// newTempRec builds a fresh starting-position tempGameRec.
func newTempRec(anonID string, thinkMS int, engineWhite, engineBlack bool) *tempGameRec {
	if thinkMS <= 0 {
		thinkMS = 1000
	}
	if !engineWhite && !engineBlack {
		// Default for the anonymous landing experience: human vs engine,
		// you play white. Engine-vs-engine via toggle later.
		engineBlack = true
	}
	now := time.Now().UnixMilli()
	return &tempGameRec{
		ID:               "temp-" + uuid.New().String(),
		OwnerAnonID:      anonID,
		History:          []string{},
		EngineWhite:      engineWhite,
		EngineBlack:      engineBlack,
		WhiteThinkTimeMS: thinkMS,
		BlackThinkTimeMS: thinkMS,
		Status:           "ongoing",
		Result:           "*",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

// ===== HANDLERS =====

// handleTempSession is the landing endpoint. Idempotent: returns the
// caller's currently-active temp game if one exists and hasn't expired,
// otherwise creates a new one. SPA calls this on first visit.
func (s *GameService) handleTempSession(w http.ResponseWriter, r *http.Request) {
	anonID := r.URL.Query().Get("anon_id")
	if anonID == "" {
		http.Error(w, "missing anon_id (gateway should inject)", http.StatusBadRequest)
		return
	}
	store := newTempStore(s.bus.Rdb())
	ctx := r.Context()

	if existingID, err := store.sessionGameID(ctx, anonID); err == nil && existingID != "" {
		if rec, err := store.get(ctx, existingID); err == nil && rec != nil && rec.OwnerAnonID == anonID {
			store.refreshTTL(ctx, existingID, anonID)
			writeJSON(w, s.tempSnapshot(ctx, rec))
			return
		}
		// stale session pointer; fall through to create
	}

	rec := newTempRec(anonID, 1000, false, true)
	if err := store.save(ctx, rec); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	if err := store.setSession(ctx, anonID, rec.ID); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, s.tempSnapshot(ctx, rec))
}

func (s *GameService) handleTempState(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := s.requireTempAccess(w, r)
	if !ok {
		return
	}
	writeJSON(w, s.tempSnapshot(r.Context(), rec))
}

func (s *GameService) handleTempMove(w http.ResponseWriter, r *http.Request) {
	rec, store, ok := s.requireTempAccess(w, r)
	if !ok {
		return
	}
	var req struct {
		Move string `json:"move"`
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

	rec, err = store.get(r.Context(), rec.ID)
	if err != nil || rec == nil {
		http.Error(w, "temp game not found or expired", http.StatusNotFound)
		return
	}
	if rec.Status != "ongoing" {
		http.Error(w, "game is already finished", http.StatusConflict)
		return
	}

	gm := game.NewGame()
	gm.Load("", rec.History, rec.EngineWhite, rec.EngineBlack)

	if gm.EngineToMove() {
		http.Error(w, "it is the engine's turn", http.StatusConflict)
		return
	}

	m, err := gm.Board.ParseUCIMove(req.Move)
	if err != nil {
		http.Error(w, "invalid move format", http.StatusBadRequest)
		return
	}
	matched, ok := game.MatchMove(gm.Board.GenerateLegalMoves(), m)
	if !ok {
		http.Error(w, "illegal move", http.StatusForbidden)
		return
	}
	gm.PlayMove(matched)

	rec.History = gm.History
	rec.Status = string(gm.Status())
	if rec.Status == "checkmate" {
		// gm.Status reports checkmate when the side-to-move has no legal
		// moves and is in check; that side LOST.
		if gm.Board.SideToMove == core.White {
			rec.Result = "0-1"
		} else {
			rec.Result = "1-0"
		}
	} else if rec.Status != "ongoing" {
		rec.Result = "1/2-1/2"
	}

	if err := store.save(r.Context(), rec); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	store.refreshTTL(r.Context(), rec.ID, rec.OwnerAnonID)

	snapshot := s.tempSnapshot(r.Context(), rec)
	s.publishTempState(r.Context(), rec.ID, snapshot)

	if gm.Status() == game.StatusOngoing && gm.EngineToMove() {
		s.triggerTempEngineMove(rec, gm)
	}

	writeJSON(w, snapshot)
}

// handleTempNew resets the *same* game ID back to the start position.
// Same ID means the existing WebSocket subscription survives — no
// reconnect needed.
func (s *GameService) handleTempNew(w http.ResponseWriter, r *http.Request) {
	rec, store, ok := s.requireTempAccess(w, r)
	if !ok {
		return
	}
	var req struct {
		EngineWhite bool `json:"engine_white"`
		EngineBlack bool `json:"engine_black"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	lock, err := acquireGameLock(r.Context(), s.bus.Rdb(), rec.ID, gameLockTTL)
	if err != nil || lock == nil {
		http.Error(w, "lock contention", http.StatusConflict)
		return
	}
	defer lock.release(context.Background())

	rec.History = []string{}
	rec.EngineWhite = req.EngineWhite
	rec.EngineBlack = req.EngineBlack || (!req.EngineWhite && !req.EngineBlack)
	rec.Status = "ongoing"
	rec.Result = "*"

	if err := store.save(r.Context(), rec); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	store.refreshTTL(r.Context(), rec.ID, rec.OwnerAnonID)
	_ = s.bus.Rdb().Del(r.Context(), "game:thinking:"+rec.ID).Err()

	snapshot := s.tempSnapshot(r.Context(), rec)
	s.publishTempState(r.Context(), rec.ID, snapshot)

	// If engine plays white, kick off its move immediately.
	gm := game.NewGame()
	gm.Load("", rec.History, rec.EngineWhite, rec.EngineBlack)
	if gm.EngineToMove() {
		s.triggerTempEngineMove(rec, gm)
	}

	writeJSON(w, snapshot)
}

func (s *GameService) handleTempResign(w http.ResponseWriter, r *http.Request) {
	rec, store, ok := s.requireTempAccess(w, r)
	if !ok {
		return
	}
	lock, err := acquireGameLock(r.Context(), s.bus.Rdb(), rec.ID, gameLockTTL)
	if err != nil || lock == nil {
		http.Error(w, "lock contention", http.StatusConflict)
		return
	}
	defer lock.release(context.Background())

	rec, err = store.get(r.Context(), rec.ID)
	if err != nil || rec == nil {
		http.Error(w, "temp game not found", http.StatusNotFound)
		return
	}
	if rec.Status != "ongoing" {
		http.Error(w, "game is already finished", http.StatusConflict)
		return
	}

	// Anonymous game = always playing as white (default), so resigning =
	// white loses. If we ever expose engine-as-white in this surface,
	// this needs to look at rec.EngineWhite.
	rec.Status = "resign"
	if rec.EngineWhite {
		rec.Result = "1-0"
	} else {
		rec.Result = "0-1"
	}

	if err := store.save(r.Context(), rec); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	store.refreshTTL(r.Context(), rec.ID, rec.OwnerAnonID)
	_ = s.bus.Rdb().Del(r.Context(), "game:thinking:"+rec.ID).Err()

	snapshot := s.tempSnapshot(r.Context(), rec)
	s.publishTempState(r.Context(), rec.ID, snapshot)
	writeJSON(w, snapshot)
}

func (s *GameService) handleTempUndo(w http.ResponseWriter, r *http.Request) {
	rec, store, ok := s.requireTempAccess(w, r)
	if !ok {
		return
	}
	lock, err := acquireGameLock(r.Context(), s.bus.Rdb(), rec.ID, gameLockTTL)
	if err != nil || lock == nil {
		http.Error(w, "lock contention", http.StatusConflict)
		return
	}
	defer lock.release(context.Background())

	rec, err = store.get(r.Context(), rec.ID)
	if err != nil || rec == nil {
		http.Error(w, "temp game not found", http.StatusNotFound)
		return
	}

	gm := game.NewGame()
	gm.Load("", rec.History, rec.EngineWhite, rec.EngineBlack)
	// Pop two moves so undo lands the user back on their own turn (the
	// engine's last reply + the user's last move). Single-ply undo
	// would leave the engine to move again, which immediately re-fires
	// the same search.
	if gm.Undo() {
		_ = gm.Undo()
	}

	rec.History = gm.History
	rec.Status = "ongoing"
	rec.Result = "*"
	if err := store.save(r.Context(), rec); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	store.refreshTTL(r.Context(), rec.ID, rec.OwnerAnonID)
	_ = s.bus.Rdb().Del(r.Context(), "game:thinking:"+rec.ID).Err()

	snapshot := s.tempSnapshot(r.Context(), rec)
	s.publishTempState(r.Context(), rec.ID, snapshot)
	writeJSON(w, snapshot)
}

// handleTempSetPlayers updates engine assignment and per-side think
// times for a temp game. Mirrors handleHTTPSetPlayers but writes back
// to the Redis tempGameRec instead of the PG row. If the change makes
// it the engine's turn (e.g. user toggled engine_black on while it was
// black to move), we kick off a search with the new think time so the
// dropdown change visibly applies.
func (s *GameService) handleTempSetPlayers(w http.ResponseWriter, r *http.Request) {
	rec, store, ok := s.requireTempAccess(w, r)
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
	if err != nil || lock == nil {
		http.Error(w, "lock contention", http.StatusConflict)
		return
	}
	defer lock.release(context.Background())

	rec, err = store.get(r.Context(), rec.ID)
	if err != nil || rec == nil {
		http.Error(w, "temp game not found", http.StatusNotFound)
		return
	}

	rec.EngineWhite = req.EngineWhite
	rec.EngineBlack = req.EngineBlack
	if req.WhiteThinkTime > 0 {
		rec.WhiteThinkTimeMS = req.WhiteThinkTime
	}
	if req.BlackThinkTime > 0 {
		rec.BlackThinkTimeMS = req.BlackThinkTime
	}

	if err := store.save(r.Context(), rec); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	store.refreshTTL(r.Context(), rec.ID, rec.OwnerAnonID)

	// If toggling engine assignment makes it the engine's turn (and the
	// game is still going), kick off a search. Same shape as the durable
	// withLockedMutation path: settings change can immediately produce a
	// new engine move.
	gm := game.NewGame()
	gm.Load("", rec.History, rec.EngineWhite, rec.EngineBlack)
	if rec.Status == "ongoing" && gm.EngineToMove() {
		// Drop any stale "thinking" sentinel — the in-flight search (if
		// any) used the OLD think time; if we're flipping sides or
		// changing engines, the user shouldn't see lingering spinner.
		_ = s.bus.Rdb().Del(r.Context(), "game:thinking:"+rec.ID).Err()
		s.triggerTempEngineMove(rec, gm)
	}

	snapshot := s.tempSnapshot(r.Context(), rec)
	s.publishTempState(r.Context(), rec.ID, snapshot)
	writeJSON(w, snapshot)
}

func (s *GameService) handleTempHint(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := s.requireTempAccess(w, r)
	if !ok {
		return
	}
	var req struct {
		MoveTime int `json:"movetime"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	moveTime := time.Duration(req.MoveTime) * time.Millisecond
	if moveTime <= 0 {
		moveTime = 1 * time.Second
	}

	gm := game.NewGame()
	gm.Load("", rec.History, rec.EngineWhite, rec.EngineBlack)

	er := eventbus.EngineRequest{
		GameID:   rec.ID,
		FEN:      gm.Board.FEN(),
		History:  game.CopyHistory(gm.HistoryHash()),
		MoveTime: moveTime,
		Context:  "hint",
		Metadata: map[string]string{"temp": "1", "anon_id": rec.OwnerAnonID},
	}
	if _, err := s.bus.SendEngineRequest(r.Context(), er); err != nil {
		http.Error(w, "dispatch failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// triggerTempEngineMove dispatches an engine search for a temp game.
// Mirrors triggerEngineForMove in handlers.go but uses the Metadata
// channel to mark the request as temp so processEngineResult routes
// the response back to applyTempEngineMove instead of CmdMakeMove.
func (s *GameService) triggerTempEngineMove(rec *tempGameRec, gm *game.Game) {
	moveTime := rec.thinkForSide(gm.Board.SideToMove)
	ttl := 2*moveTime + 2*time.Second
	_ = s.bus.Rdb().Set(context.Background(), "game:thinking:"+rec.ID, "1", ttl).Err()

	req := eventbus.EngineRequest{
		GameID:   rec.ID,
		FEN:      gm.Board.FEN(),
		History:  game.CopyHistory(gm.HistoryHash()),
		MoveTime: moveTime,
		Context:  "move",
		Metadata: map[string]string{"temp": "1", "anon_id": rec.OwnerAnonID},
	}
	if _, err := s.bus.SendEngineRequest(context.Background(), req); err != nil {
		slog.Error("dispatch temp engine request failed", "game_id", rec.ID, "error", err)
		_ = s.bus.Rdb().Del(context.Background(), "game:thinking:"+rec.ID).Err()
	}
}

// applyTempEngineMove is the engine→temp result path. Called from
// processEngineResult when the response Metadata carries temp=1.
// Acquires the per-game lock, applies the move, persists, refreshes
// TTL, and publishes on game.evt.{id}.
func (s *GameService) applyTempEngineMove(ctx context.Context, resp eventbus.EngineResponse) {
	store := newTempStore(s.bus.Rdb())
	lock, err := acquireGameLock(ctx, s.bus.Rdb(), resp.GameID, gameLockTTL)
	if err != nil || lock == nil {
		slog.Warn("temp engine result: lock unavailable", "game_id", resp.GameID)
		return
	}
	defer lock.release(context.Background())

	rec, err := store.get(ctx, resp.GameID)
	if err != nil || rec == nil {
		// Game expired or never existed — drop the result silently.
		return
	}
	if rec.Status != "ongoing" {
		// User resigned (or game otherwise terminated) while engine was
		// thinking; drop the late move so the SPA doesn't see the board
		// jump back to "ongoing" with the engine's reply applied.
		return
	}

	gm := game.NewGame()
	gm.Load("", rec.History, rec.EngineWhite, rec.EngineBlack)
	m, err := gm.Board.ParseUCIMove(resp.BestMove)
	if err != nil {
		slog.Warn("temp engine: invalid move format", "move", resp.BestMove)
		return
	}
	matched, ok := game.MatchMove(gm.Board.GenerateLegalMoves(), m)
	if !ok {
		slog.Warn("temp engine: illegal move", "game_id", rec.ID, "move", resp.BestMove)
		return
	}
	gm.PlayMove(matched)

	rec.History = gm.History
	rec.Status = string(gm.Status())
	if rec.Status == "checkmate" {
		if gm.Board.SideToMove == core.White {
			rec.Result = "0-1"
		} else {
			rec.Result = "1-0"
		}
	} else if rec.Status != "ongoing" {
		rec.Result = "1/2-1/2"
	}

	if err := store.save(ctx, rec); err != nil {
		slog.Error("temp engine save failed", "game_id", rec.ID, "error", err)
		return
	}
	store.refreshTTL(ctx, rec.ID, rec.OwnerAnonID)
	_ = s.bus.Rdb().Del(ctx, "game:thinking:"+rec.ID).Err()

	s.publishTempState(ctx, rec.ID, s.tempSnapshot(ctx, rec))
}

// publishTempState fans the new snapshot out via Redis Pub/Sub on the
// canonical game.evt.{id} channel — same channel durable games use, so
// the gateway's hub PSUBSCRIBE picks it up with no extra wiring.
func (s *GameService) publishTempState(ctx context.Context, gameID string, snapshot stateJSON) {
	snapPayload, _ := json.Marshal(snapshot)
	evt := eventbus.Event{
		Type:    eventbus.EvtStateUpdated,
		GameID:  gameID,
		Payload: snapPayload,
	}
	data, _ := json.Marshal(evt)
	_ = s.bus.Rdb().Publish(ctx, "game.evt."+gameID, data).Err()
}

// tempSnapshot mirrors snapshotFromRecord but reads from a tempGameRec.
// Returns the same wire shape so the SPA can use the existing GameView
// component with no field-level changes.
func (s *GameService) tempSnapshot(ctx context.Context, rec *tempGameRec) stateJSON {
	gm := game.NewGame()
	gm.Load("", rec.History, rec.EngineWhite, rec.EngineBlack)

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

	status := string(gm.Status())
	if rec.Status != "" && rec.Status != "ongoing" {
		status = rec.Status
	}

	thinking := false
	if v, err := s.bus.Rdb().Get(ctx, "game:thinking:"+rec.ID).Result(); err == nil && v == "1" {
		thinking = true
	}

	return stateJSON{
		ID:             rec.ID,
		FEN:            gm.Board.FEN(),
		Turn:           turn,
		EngineWhite:    gm.EngineWhite,
		EngineBlack:    gm.EngineBlack,
		EngineToMove:   gm.EngineToMove(),
		Status:         status,
		Result:         rec.Result,
		InCheck:        gm.Board.InCheck(gm.Board.SideToMove),
		LegalMoves:     legalStrs,
		History:        gm.History,
		HistorySAN:     gm.HistorySAN,
		LastMove:       last,
		Thinking:       thinking,
		WhiteThinkTime: int(rec.thinkForSide(core.White) / time.Millisecond),
		BlackThinkTime: int(rec.thinkForSide(core.Black) / time.Millisecond),
		TimeControl:    "engine",
		Rated:          false,
	}
}
