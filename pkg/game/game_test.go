package game

import (
	"testing"

	"github.com/neoromantics/chess/pkg/core"
)

func mustMove(t *testing.T, g *Game, ms string) {
	t.Helper()
	m, err := g.Board.ParseUCIMove(ms)
	if err != nil {
		t.Fatalf("parse %s: %v", ms, err)
	}
	matched, ok := MatchMove(g.Board.GenerateLegalMoves(), m)
	if !ok {
		t.Fatalf("illegal move %s in %s", ms, g.Board.FEN())
	}
	g.PlayMove(matched)
}

func TestNewGameStart(t *testing.T) {
	g := NewGame()
	if g.Status() != StatusOngoing {
		t.Errorf("startpos status = %s, want ongoing", g.Status())
	}
	if g.Board.FEN() != core.StartFEN {
		t.Errorf("startpos FEN = %s", g.Board.FEN())
	}
	if len(g.UndoStack) != 0 || len(g.History) != 0 {
		t.Error("startpos should have empty history")
	}
}

func TestPlayMoveAppendsHistoryAndUndo(t *testing.T) {
	g := NewGame()
	mustMove(t, g, "e2e4")
	if len(g.History) != 1 || g.History[0] != "e2e4" {
		t.Errorf("history = %v", g.History)
	}
	if len(g.UndoStack) != 1 {
		t.Errorf("undoStack len = %d", len(g.UndoStack))
	}
	if g.LastMove == nil || g.LastMove.String() != "e2e4" {
		t.Errorf("lastMove = %v", g.LastMove)
	}
}

func TestUndoReverts(t *testing.T) {
	g := NewGame()
	mustMove(t, g, "e2e4")
	mustMove(t, g, "e7e5")
	if !g.Undo() {
		t.Fatal("Undo returned false")
	}
	if g.Board.SideToMove != core.Black {
		t.Errorf("after one undo, side to move = %d, want Black", g.Board.SideToMove)
	}
	if !g.Undo() {
		t.Fatal("second Undo returned false")
	}
	if g.Board.FEN() != core.StartFEN {
		t.Errorf("after both undone, FEN = %s", g.Board.FEN())
	}
	if g.Undo() {
		t.Error("Undo on empty stack should return false")
	}
}

func TestStatusCheckmate(t *testing.T) {
	g := NewGame()
	for _, m := range []string{"f2f3", "e7e5", "g2g4", "d8h4"} {
		mustMove(t, g, m)
	}
	if g.Status() != StatusCheckmate {
		t.Errorf("status after fool's mate = %s, want checkmate", g.Status())
	}
}

func TestStatusStalemate(t *testing.T) {
	g := NewGame()
	if err := g.Load("7k/5Q2/6K1/8/8/8/8/8 b - - 0 1", nil, false, false); err != nil {
		t.Fatal(err)
	}
	if g.Status() != StatusStalemate {
		t.Errorf("status = %s, want stalemate", g.Status())
	}
}

func TestStatusInsufficientMaterial(t *testing.T) {
	cases := []struct {
		name string
		fen  string
		want GameStatus
	}{
		{"K vs K", "4k3/8/8/8/8/8/8/4K3 w - - 0 1", StatusDrawInsufficient},
		{"K vs KN", "4k3/8/8/8/8/8/8/4K2N w - - 0 1", StatusDrawInsufficient},
		{"K+B vs K+B same color", "4k2b/8/8/8/8/8/8/2B1K3 w - - 0 1", StatusDrawInsufficient},
		{"K+B vs K+B opposite", "4k1b1/8/8/8/8/8/8/2B1K3 w - - 0 1", StatusOngoing},
		{"K vs K+R", "4k3/8/8/8/8/8/8/4K2R w - - 0 1", StatusOngoing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewGame()
			if err := g.Load(tc.fen, nil, false, false); err != nil {
				t.Fatal(err)
			}
			if got := g.Status(); got != tc.want {
				t.Errorf("status = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestStatusThreefoldRepetition(t *testing.T) {
	g := NewGame()
	// Knight shuffles: g1f3 g8f6 f3g1 f6g8 (back to startpos, count=2)
	// repeat: count=3 → draw
	for _, m := range []string{"g1f3", "g8f6", "f3g1", "f6g8", "g1f3", "g8f6", "f3g1", "f6g8"} {
		mustMove(t, g, m)
	}
	if g.Status() != StatusDrawRepetition {
		t.Errorf("status = %s, want draw_repetition", g.Status())
	}
}

func TestRepetitionResetOnPawnMove(t *testing.T) {
	g := NewGame()
	// Reach a position twice via shuffles, then play a pawn move; the
	// counter must flush so a later return doesn't falsely trigger draw.
	for _, m := range []string{"g1f3", "g8f6", "f3g1", "f6g8"} {
		mustMove(t, g, m)
	}
	mustMove(t, g, "e2e4") // irreversible: counter flushes
	mustMove(t, g, "e7e5")
	for _, m := range []string{"g1f3", "g8f6", "f3g1", "f6g8"} {
		mustMove(t, g, m)
	}
	// We've returned to "startpos + e2e4 e7e5" twice (after 1.e4 e5 and again
	// after the shuffles) — that's count 2 in the new segment. Should NOT be
	// a draw yet.
	if g.Status() != StatusOngoing {
		t.Errorf("status = %s, want ongoing (only 2 occurrences in new segment)", g.Status())
	}
}

func TestTouchCommits(t *testing.T) {
	g := NewGame()
	g.TouchMove = true
	g.Touch(core.Sq(4, 1)) // e2 — has legal moves
	if g.TouchedSq != core.Sq(4, 1) {
		t.Errorf("TouchedSq = %d, want e2 (%d)", g.TouchedSq, core.Sq(4, 1))
	}
	if g.TouchLost {
		t.Error("TouchLost set after touching a movable piece")
	}
}

func TestTouchImmovablePieceLoses(t *testing.T) {
	g := NewGame()
	g.TouchMove = true
	// Black to move. f7 pawn is blocked by f6 white pawn and cannot capture.
	if err := g.Load("6k1/5p2/5P2/8/8/8/8/4K3 b - - 0 1", nil, false, false); err != nil {
		t.Fatal(err)
	}
	g.Touch(core.Sq(5, 6)) // f7
	if !g.TouchLost {
		t.Error("touching the immovable f7 pawn should set TouchLost")
	}
	if g.Status() != StatusTouchLost {
		t.Errorf("status = %s, want touch_lost", g.Status())
	}
}

func TestLastHumanMoveIndexSkipsEngine(t *testing.T) {
	g := NewGame()
	g.EngineBlack = true // White=human, Black=engine
	for _, m := range []string{"e2e4", "e7e5", "g1f3", "b8c6"} {
		mustMove(t, g, m)
	}
	// Index 0 (e2e4, White) and 2 (g1f3, White) are human; 1 and 3 are engine.
	got := g.LastHumanMoveIndex()
	if got != 2 {
		t.Errorf("LastHumanMoveIndex = %d, want 2 (g1f3 by human White)", got)
	}
}

func TestPlayerAt(t *testing.T) {
	g := NewGame()
	for _, m := range []string{"e2e4", "e7e5", "g1f3"} {
		mustMove(t, g, m)
	}
	if g.PlayerAt(0) != core.White {
		t.Errorf("PlayerAt(0) = %d, want White", g.PlayerAt(0))
	}
	if g.PlayerAt(1) != core.Black {
		t.Errorf("PlayerAt(1) = %d, want Black", g.PlayerAt(1))
	}
	if g.PlayerAt(2) != core.White {
		t.Errorf("PlayerAt(2) = %d, want White", g.PlayerAt(2))
	}
}

func TestLoadRejectsBadFEN(t *testing.T) {
	g := NewGame()
	if err := g.Load("not a fen", nil, false, false); err == nil {
		t.Error("Load with bad FEN should return error")
	}
}

func TestBoardsAroundMovePreservesLiveBoard(t *testing.T) {
	g := NewGame()
	for _, m := range []string{"e2e4", "e7e5", "g1f3"} {
		mustMove(t, g, m)
	}
	liveBefore := g.Board.FEN()
	before, after := g.BoardsAroundMove(0) // before/after 1.e4
	if liveBefore != g.Board.FEN() {
		t.Errorf("live board mutated: %s -> %s", liveBefore, g.Board.FEN())
	}
	if before.FEN() != core.StartFEN {
		t.Errorf("before(0) = %s, want startpos", before.FEN())
	}
	wantAfter := "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1"
	if after.FEN() != wantAfter {
		t.Errorf("after(0) = %s, want %s", after.FEN(), wantAfter)
	}
}

func TestLoadReplaysMoves(t *testing.T) {
	g := NewGame()
	if err := g.Load(core.StartFEN, []string{"e2e4", "e7e5"}, false, false); err != nil {
		t.Fatal(err)
	}
	if len(g.History) != 2 {
		t.Errorf("history len = %d, want 2", len(g.History))
	}
}
