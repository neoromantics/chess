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
	gameID := r.URL.Query().Get("game_id")
	if gameID == "" {
		http.Error(w, "missing game_id", http.StatusBadRequest)
		return nil, 0, false
	}
	rec, err := s.db.GetGame(gameID)
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
	rec, err = s.db.GetGame(rec.ID)
	if err != nil {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}

	gm := game.NewGame()
	var history, historySAN []string
	_ = json.Unmarshal([]byte(rec.History), &history)
	_ = json.Unmarshal([]byte(rec.HistorySAN), &historySAN)
	gm.Load(rec.FEN, history, rec.EngineWhite, rec.EngineBlack)
	gm.HistorySAN = historySAN

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
	rec.Status = string(gm.Status())
	rec.UpdatedAt = time.Now()
	if err := s.db.SaveGame(rec); err != nil {
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
	s.withLockedMutation(w, r, func(gm *game.Game, rec *db.GameRecord) error {
		var req struct {
			Move string `json:"move"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return userErr(http.StatusBadRequest, "invalid body")
		}
		if gm.Status() != game.StatusOngoing {
			return userErr(http.StatusConflict, "game is already finished")
		}
		if gm.EngineToMove() {
			return userErr(http.StatusConflict, "it is the engine's turn")
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

// ===== NEW (reset same row) =====

func (s *GameService) handleHTTPNew(w http.ResponseWriter, r *http.Request) {
	s.withLockedMutation(w, r, func(gm *game.Game, rec *db.GameRecord) error {
		var req struct {
			EngineWhite bool `json:"engine_white"`
			EngineBlack bool `json:"engine_black"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gm.Reset()
		gm.EngineWhite = req.EngineWhite
		gm.EngineBlack = req.EngineBlack
		return nil
	})
}

// ===== SET_PLAYERS =====

func (s *GameService) handleHTTPSetPlayers(w http.ResponseWriter, r *http.Request) {
	s.withLockedMutation(w, r, func(gm *game.Game, rec *db.GameRecord) error {
		var req struct {
			EngineWhite    bool `json:"engine_white"`
			EngineBlack    bool `json:"engine_black"`
			WhiteThinkTime int  `json:"white_think_time"`
			BlackThinkTime int  `json:"black_think_time"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return userErr(http.StatusBadRequest, "invalid body")
		}
		gm.EngineWhite = req.EngineWhite
		gm.EngineBlack = req.EngineBlack
		if req.WhiteThinkTime > 0 {
			rec.WhiteThinkTime = req.WhiteThinkTime
		}
		if req.BlackThinkTime > 0 {
			rec.BlackThinkTime = req.BlackThinkTime
		}
		return nil
	})
}

// ===== UNDO =====

func (s *GameService) handleHTTPUndo(w http.ResponseWriter, r *http.Request) {
	s.withLockedMutation(w, r, func(gm *game.Game, rec *db.GameRecord) error {
		gm.Undo()
		return nil
	})
}

// ===== TOUCH =====

func (s *GameService) handleHTTPTouch(w http.ResponseWriter, r *http.Request) {
	s.withLockedMutation(w, r, func(gm *game.Game, rec *db.GameRecord) error {
		var req struct {
			Square string `json:"square"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return userErr(http.StatusBadRequest, "invalid body")
		}
		sq, _ := core.ParseSquare(req.Square)
		gm.Touch(sq)
		return nil
	})
}

// ===== TOUCH_MOVE TOGGLE =====

func (s *GameService) handleHTTPTouchMove(w http.ResponseWriter, r *http.Request) {
	s.withLockedMutation(w, r, func(gm *game.Game, rec *db.GameRecord) error {
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return userErr(http.StatusBadRequest, "invalid body")
		}
		gm.TouchMove = req.Enabled
		return nil
	})
}

// ===== LOAD =====

func (s *GameService) handleHTTPLoad(w http.ResponseWriter, r *http.Request) {
	s.withLockedMutation(w, r, func(gm *game.Game, rec *db.GameRecord) error {
		var req struct {
			StartFEN    string   `json:"start_fen"`
			Moves       []string `json:"moves"`
			EngineWhite bool     `json:"engine_white"`
			EngineBlack bool     `json:"engine_black"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return userErr(http.StatusBadRequest, "invalid body")
		}
		gm.Load(req.StartFEN, req.Moves, req.EngineWhite, req.EngineBlack)
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
	if rows == 0 {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ===== SAVE (export) =====

func (s *GameService) handleHTTPSave(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := s.requireGameAccess(w, r)
	if !ok {
		return
	}
	var history, historySAN []string
	_ = json.Unmarshal([]byte(rec.History), &history)
	_ = json.Unmarshal([]byte(rec.HistorySAN), &historySAN)
	var assessments []any
	_ = json.Unmarshal([]byte(rec.Assessments), &assessments)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=chess-game.json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"game_id":      rec.ID,
		"start_fen":    rec.FEN, // best-effort; the row only stores current FEN
		"moves":        history,
		"history_san":  historySAN,
		"engine_white": rec.EngineWhite,
		"engine_black": rec.EngineBlack,
		"assessments":  assessments,
		"exported_at":  time.Now().UTC().Format(time.RFC3339),
	})
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
	gm.Load(rec.FEN, history, rec.EngineWhite, rec.EngineBlack)

	er := eventbus.EngineRequest{
		GameID:   rec.ID,
		FEN:      gm.Board.FEN(),
		History:  game.CopyHistory(gm.HistoryHash()),
		MoveTime: moveTime,
		Context:  "hint",
	}
	if _, err := s.bus.SendEngineRequest(r.Context(), er); err != nil {
		http.Error(w, "dispatch failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// ===== ASSESS (async) =====

func (s *GameService) handleHTTPAssess(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := s.requireGameAccess(w, r)
	if !ok {
		return
	}
	var req struct {
		MoveTime int  `json:"movetime"`
		Index    *int `json:"index"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	moveTime := time.Duration(req.MoveTime) * time.Millisecond
	if moveTime <= 0 {
		moveTime = 1000 * time.Millisecond
	}

	var history []string
	_ = json.Unmarshal([]byte(rec.History), &history)
	gm := game.NewGame()
	gm.Load(rec.FEN, history, rec.EngineWhite, rec.EngineBlack)

	er := eventbus.EngineRequest{
		GameID:   rec.ID,
		FEN:      gm.Board.FEN(),
		History:  game.CopyHistory(gm.HistoryHash()),
		MoveTime: moveTime,
		Context:  "assess",
	}
	if _, err := s.bus.SendEngineRequest(r.Context(), er); err != nil {
		http.Error(w, "dispatch failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
