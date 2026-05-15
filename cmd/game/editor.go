package main

// Board editor — POST /api/set_position lets a single player (engine
// game owner) replace the position outright with a FEN. Stored as
// rec.StartFEN; history is wiped; the engine takes over if the new
// side to move is an engine. PvP games are rejected: setting up an
// arbitrary position would undermine rating fairness, and the clock
// semantics of "start from this endgame with the original time bank"
// are not what either player expects.
//
// Bypasses withLockedMutation because we are NOT advancing a move —
// we're discarding history entirely. withLockedMutation's clock-tick
// and engine-trigger logic both key off "history grew by one"; for
// set_position we tear the row down and rebuild it inside the same
// lock.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/game"
)

func (s *GameService) handleHTTPSetPosition(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := s.requireGameAccess(w, r)
	if !ok {
		return
	}
	// PvP games can't be re-set; only engine games (at least one side
	// is the engine) qualify. Use the same "both human players" test
	// the takeback path uses.
	if rec.WhiteUserID != nil && rec.BlackUserID != nil {
		http.Error(w, "set_position is only available for engine games", http.StatusBadRequest)
		return
	}

	var req struct {
		FEN string `json:"fen"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if _, err := core.ParseFEN(req.FEN); err != nil {
		http.Error(w, "invalid FEN: "+err.Error(), http.StatusBadRequest)
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
	rec.StartFEN = req.FEN
	rec.FEN = req.FEN
	rec.History = "[]"
	rec.HistorySAN = "[]"
	rec.Status = "ongoing"
	rec.Result = "*"
	rec.UpdatedAt = time.Now()
	if err := s.saveGameCached(r.Context(), rec); err != nil {
		slog.Error("set_position save failed", "game_id", rec.ID, "error", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	// Any in-flight engine search for the OLD position is now stale.
	_ = s.bus.Rdb().Del(context.Background(), "game:thinking:"+rec.ID).Err()

	snapshot := s.snapshotFromRecord(r.Context(), rec)
	snapPayload, _ := json.Marshal(snapshot)
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type: eventbus.EvtStateUpdated, GameID: rec.ID, Payload: snapPayload,
	})

	// If the new position has the engine on the move, kick off a
	// search immediately so the SPA isn't waiting on the user to
	// "ping" the engine.
	gm := game.NewGame()
	gm.Load(rec.StartFEN, nil, rec.EngineWhite, rec.EngineBlack)
	if gm.Status() == game.StatusOngoing && gm.EngineToMove() {
		s.triggerEngineForMove(rec, gm)
	}

	writeJSON(w, snapshot)
}
