package main

// Tests for handleHTTPMove — the workhorse of the SPA's move pipeline.
// Every legal-move check, every per-side-to-move authz, every cache
// + store write-through goes through this handler. Until this commit
// it had zero automated coverage.
//
// What these tests deliberately don't cover:
//   - Clock interactions (TimeControl="engine" suppresses the clock
//     hash, so loadClock returns nil and that branch is a no-op).
//     Clock tests deserve their own file.
//   - Engine retrigger (we use a fresh PvP game so EngineToMove is
//     false and the dispatch path stays dormant).
//   - The matchmaker-engine-fallback bot disguise. Future test once
//     the bot pool's TODOs collapse.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/neoromantics/chess/pkg/db"
)

const (
	testAliceID int64 = 1001
	testBobID   int64 = 1002
)

// newPvPGame builds a fresh PvP record at the standard start position
// with Alice (white) and Bob (black). UpdatedAt set to a stable past
// timestamp so snapshot Rev comparisons are deterministic.
func newPvPGame() *db.GameRecord {
	alice := testAliceID
	bob := testBobID
	return &db.GameRecord{
		ID:             uuid.New().String(),
		WhiteUserID:    &alice,
		BlackUserID:    &bob,
		FEN:            "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
		StartFEN:       "",
		History:        "[]",
		HistorySAN:     "[]",
		WhiteThinkTime: 1000,
		BlackThinkTime: 1000,
		TimeControl:    "engine", // suppresses clock activation for these tests
		Rated:          false,
		Status:         "ongoing",
		Result:         "*",
		Assessments:    "[]",
		CreatedAt:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// newMoveRequest builds a POST /api/games/{id}/move request with the
// path value pre-bound (the production server uses Go 1.22 PathValue;
// in tests we set it manually so we don't have to spin up a mux).
func newMoveRequest(t *testing.T, gameID, move string, userID int64) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"move": move})
	r := httptest.NewRequest(http.MethodPost, "/api/games/"+gameID+"/move", bytes.NewReader(body))
	r.SetPathValue("id", gameID)
	if userID != 0 {
		r.Header.Set("X-User-ID", strconv.FormatInt(userID, 10))
	}
	return httptest.NewRecorder(), r
}

func decodeSnapshot(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var snap map[string]any
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("decode snapshot: %v\nbody: %s", err, string(body))
	}
	return snap
}

func TestHandleHTTPMove_HappyPath(t *testing.T) {
	gs := newGameStore()
	s, _ := newTestService(t, gs)
	rec := newPvPGame()
	seedGame(t, s, gs, rec)

	w, r := newMoveRequest(t, rec.ID, "e2e4", testAliceID)
	s.handleHTTPMove(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%q", w.Code, w.Body.String())
	}
	snap := decodeSnapshot(t, w.Body.Bytes())
	if turn := snap["turn"]; turn != "b" {
		t.Errorf("after white move, turn should be 'b', got %v", turn)
	}
	if status := snap["status"]; status != "ongoing" {
		t.Errorf("status after legal move: got %v, want 'ongoing'", status)
	}
	if len(gs.saves) != 1 {
		t.Errorf("expected exactly 1 SaveGame call, got %d", len(gs.saves))
	}
	// Verify the saved record's FEN advanced past the start position.
	if got := gs.saves[0].FEN; got == "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1" {
		t.Errorf("saved FEN is still the start position — move never applied")
	}
}

func TestHandleHTTPMove_RejectsOpponentsMove(t *testing.T) {
	// Bob (black) tries to play a white move while white is on move.
	// Per-side-to-move authz must return 409 — without this the white
	// side could be moved by the black player.
	gs := newGameStore()
	s, _ := newTestService(t, gs)
	rec := newPvPGame()
	seedGame(t, s, gs, rec)

	w, r := newMoveRequest(t, rec.ID, "e2e4", testBobID)
	s.handleHTTPMove(w, r)

	if w.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409 Conflict (it's not bob's turn); body=%q",
			w.Code, w.Body.String())
	}
	if len(gs.saves) != 0 {
		t.Errorf("opponent-move rejection wrote to store — got %d saves", len(gs.saves))
	}
}

func TestHandleHTTPMove_RejectsNonParticipant(t *testing.T) {
	// A random signed-in user who doesn't own the game. requireGameAccess
	// must 404 (not 403) so existence doesn't leak via status code.
	gs := newGameStore()
	s, _ := newTestService(t, gs)
	rec := newPvPGame()
	seedGame(t, s, gs, rec)

	var carolID int64 = 9999
	w, r := newMoveRequest(t, rec.ID, "e2e4", carolID)
	s.handleHTTPMove(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404 (existence leak prevention); body=%q",
			w.Code, w.Body.String())
	}
	if len(gs.saves) != 0 {
		t.Errorf("non-participant write hit the store — got %d saves", len(gs.saves))
	}
}

func TestHandleHTTPMove_RejectsIllegalMove(t *testing.T) {
	// Well-formed UCI ("e2e5" — three squares forward in one ply) but
	// not in the legal-move list. Must 403 (the handler's existing
	// behavior — semantically odd, but documented now) and leave the
	// row untouched.
	gs := newGameStore()
	s, _ := newTestService(t, gs)
	rec := newPvPGame()
	seedGame(t, s, gs, rec)

	w, r := newMoveRequest(t, rec.ID, "e2e5", testAliceID)
	s.handleHTTPMove(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 (illegal move); body=%q",
			w.Code, w.Body.String())
	}
	if len(gs.saves) != 0 {
		t.Errorf("illegal move write-through to store — got %d saves", len(gs.saves))
	}
}

func TestHandleHTTPMove_RejectsAfterCheckmate(t *testing.T) {
	// Game already finished — even a legal-looking move attempt must
	// 409 so a resigned player can't resurrect the game by sending one
	// more move.
	gs := newGameStore()
	s, _ := newTestService(t, gs)
	rec := newPvPGame()
	rec.Status = "checkmate"
	rec.Result = "1-0"
	seedGame(t, s, gs, rec)

	w, r := newMoveRequest(t, rec.ID, "e2e4", testAliceID)
	s.handleHTTPMove(w, r)

	if w.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409 (game over); body=%q",
			w.Code, w.Body.String())
	}
	if len(gs.saves) != 0 {
		t.Errorf("finished-game move wrote to store — got %d saves", len(gs.saves))
	}
}

func TestHandleHTTPMove_RejectsUnauthenticated(t *testing.T) {
	// No X-User-ID header at all — gateway must have failed to inject
	// it, so the handler should bail with 401. Real production
	// callers always have it; this guards against a misconfigured
	// proxy silently letting through anonymous mutations.
	gs := newGameStore()
	s, _ := newTestService(t, gs)
	rec := newPvPGame()
	seedGame(t, s, gs, rec)

	w, r := newMoveRequest(t, rec.ID, "e2e4", 0) // userID=0 omits the header
	s.handleHTTPMove(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401 (no X-User-ID); body=%q",
			w.Code, w.Body.String())
	}
	if len(gs.saves) != 0 {
		t.Errorf("unauthenticated move wrote to store — got %d saves", len(gs.saves))
	}
}
