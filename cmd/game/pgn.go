package main

// PGN save / load. Save streams the current game as a download; Load
// validates a pasted PGN, sets the position to the PGN's [FEN] (if
// any), replays every move through the engine, and persists the
// resulting row. Same engine-only / no-PvP rule as the board editor —
// arbitrary positions break rating fairness, and the PGN's headers
// don't carry our internal players/clock concepts.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/game"
	"github.com/neoromantics/chess/pkg/pgn"
)

// resolveUsername returns the display name for the PGN header. Falls
// back to "engine" for nil userID (engine-played side) and "?" when a
// human user can't be resolved (PG outage, deleted account, etc.).
func (s *GameService) resolveUsername(userID *int64) string {
	if userID == nil {
		return "engine"
	}
	u, err := s.db.GetUserByID(*userID)
	if err != nil || u == nil {
		return "?"
	}
	return u.Username
}

// pgnHeadersFor builds the Seven Tag Roster from a game record.
func (s *GameService) pgnHeadersFor(rec *db.GameRecord) pgn.Headers {
	return pgn.Headers{
		Event:  "Casual Game",
		Site:   "chess.vcm-50800.vm.duke.edu",
		Date:   pgn.FormatDate(rec.CreatedAt),
		Round:  "-",
		White:  s.resolveUsername(rec.WhiteUserID),
		Black:  s.resolveUsername(rec.BlackUserID),
		Result: defaultResult(rec.Result),
	}
}

// defaultResult turns an empty / "*" stored result into the explicit
// PGN "*" token, leaving real results untouched.
func defaultResult(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "*"
	}
	return s
}

func (s *GameService) handleHTTPDownloadPGN(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := s.requireGameAccess(w, r)
	if !ok {
		return
	}
	var historySAN []string
	_ = json.Unmarshal([]byte(rec.HistorySAN), &historySAN)
	body := pgn.Encode(s.pgnHeadersFor(rec), rec.StartFEN, historySAN, defaultResult(rec.Result))

	w.Header().Set("Content-Type", "application/x-chess-pgn; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.pgn"`, rec.ID))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write([]byte(body))
}

// handleHTTPLoadPGN accepts {"pgn": "..."} and replaces the game's
// position + history with the parsed contents. Same lock / ownership
// guards as set_position. PvP rejected.
func (s *GameService) handleHTTPLoadPGN(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := s.requireGameAccess(w, r)
	if !ok {
		return
	}
	if rec.WhiteUserID != nil && rec.BlackUserID != nil {
		http.Error(w, "load_pgn is only available for engine games", http.StatusBadRequest)
		return
	}
	var req struct {
		PGN string `json:"pgn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.PGN) == "" {
		http.Error(w, "invalid body: expected {\"pgn\":\"...\"}", http.StatusBadRequest)
		return
	}
	dec, err := pgn.Decode(req.PGN)
	if err != nil {
		http.Error(w, "invalid PGN: "+err.Error(), http.StatusBadRequest)
		return
	}

	lock, lockErr := acquireGameLock(r.Context(), s.bus.Rdb(), rec.ID, gameLockTTL)
	if lockErr != nil {
		http.Error(w, "lock acquire failed", http.StatusInternalServerError)
		return
	}
	if lock == nil {
		http.Error(w, "another writer is mutating this game; retry", http.StatusConflict)
		return
	}
	defer lock.release(r.Context())

	rec, err = s.getGameCached(r.Context(), rec.ID)
	if err != nil {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}

	// Replay through a Game so we can recompute HistorySAN and end-of-
	// game status (checkmate / stalemate / draw rules) from the loaded
	// moves. Decode already validated each move was legal in turn.
	gm := game.NewGame()
	if err := gm.Load(dec.StartFEN, dec.UCIMoves, rec.EngineWhite, rec.EngineBlack); err != nil {
		http.Error(w, "replay failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	histJSON, _ := json.Marshal(gm.History)
	sanJSON, _ := json.Marshal(gm.HistorySAN)
	rec.StartFEN = dec.StartFEN
	rec.FEN = gm.Board.FEN()
	rec.History = string(histJSON)
	rec.HistorySAN = string(sanJSON)
	// Status from the engine wins over the PGN's [Result] tag — a PGN
	// can claim "1-0" while the moves stop mid-game. Trust the board.
	rec.Status = string(gm.Status())
	if rec.Status != "ongoing" {
		// Use PGN-supplied result for terminal positions if the engine
		// status doesn't encode one (e.g. resign-style PGN where the
		// last position is just "ongoing" but the file says 0-1).
		rec.Result = dec.Result
	} else {
		rec.Result = "*"
	}
	rec.UpdatedAt = time.Now()
	// Mark the row imported so its result doesn't count toward the
	// loader's wins/losses. The PGN can encode any historical result
	// for any historical game; without this gate, anyone could load
	// a Magnus PGN and inflate their stats. CountUserGameStats filters
	// on imported=FALSE; /api/new resets the flag when the row is
	// wiped, so a fresh game played on the same row id counts again.
	rec.Imported = true
	if err := s.saveGameCached(r.Context(), rec); err != nil {
		slog.Error("load_pgn save failed", "game_id", rec.ID, "error", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	_ = s.bus.Rdb().Del(r.Context(), "game:thinking:"+rec.ID).Err()

	snapshot := s.snapshotFromRecord(r.Context(), rec)
	snapPayload, _ := json.Marshal(snapshot)
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type: eventbus.EvtStateUpdated, GameID: rec.ID, Payload: snapPayload,
	})

	// Same engine-kick rule as set_position: if the loaded position has
	// the engine on the move, start a search.
	gm2 := game.NewGame()
	gm2.Load(rec.StartFEN, gm.History, rec.EngineWhite, rec.EngineBlack)
	if gm2.Status() == game.StatusOngoing && gm2.EngineToMove() {
		s.triggerEngineForMove(rec, gm2)
	}

	writeJSON(w, snapshot)
}
