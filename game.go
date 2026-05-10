package main

import (
	"fmt"
	"strings"
)

// GameStatus is the outcome of the current position.
type GameStatus string

const (
	StatusOngoing          GameStatus = "ongoing"
	StatusCheckmate        GameStatus = "checkmate"
	StatusStalemate        GameStatus = "stalemate"
	StatusDraw50           GameStatus = "draw50"
	StatusDrawRepetition   GameStatus = "draw_repetition"
	StatusDrawInsufficient GameStatus = "draw_insufficient"
	StatusTouchLost        GameStatus = "touch_lost"
)

// Game is the live chess game plus play-mode flags. It is *not* safe for
// concurrent use; the HTTP layer wraps it with a mutex.
type Game struct {
	Board     *Board
	StartFEN  string
	History   []string // long-algebraic moves, parallel to UndoStack
	LastMove  *Move
	UndoStack []Undo

	EngineWhite bool
	EngineBlack bool

	// Tournament-style touch-move rule (opt-in).
	TouchMove bool
	TouchedSq int  // NoSquare if no piece is currently touched
	TouchLost bool // sticky once a touch-move loss is declared

	// Repetition counter keyed by FEN minus halfmove/fullmove.
	posCount map[string]int
}

// NewGame returns a fresh game in the standard starting position.
func NewGame() *Game {
	g := &Game{TouchedSq: NoSquare}
	g.Reset()
	return g
}

// Reset returns the game to the standard starting position. Mode flags
// (engine sides, touch-move) are preserved.
func (g *Game) Reset() {
	g.Board = StartPosition()
	g.StartFEN = StartFEN
	g.History = nil
	g.LastMove = nil
	g.UndoStack = nil
	g.TouchedSq = NoSquare
	g.TouchLost = false
	g.posCount = map[string]int{positionKey(g.Board): 1}
}

// Load reinitialises from a starting FEN and replays moves. Engine-side
// flags come from the caller; touch-move preference is preserved.
func (g *Game) Load(startFEN string, moves []string, engineW, engineB bool) error {
	if startFEN == "" {
		startFEN = StartFEN
	}
	b, err := ParseFEN(startFEN)
	if err != nil {
		return fmt.Errorf("bad start FEN: %w", err)
	}
	g.Board = b
	g.StartFEN = startFEN
	g.History = nil
	g.LastMove = nil
	g.UndoStack = nil
	g.TouchedSq = NoSquare
	g.TouchLost = false
	g.EngineWhite = engineW
	g.EngineBlack = engineB
	g.posCount = map[string]int{positionKey(g.Board): 1}
	for _, ms := range moves {
		m, err := g.Board.ParseUCIMove(ms)
		if err != nil {
			return fmt.Errorf("bad move %s: %w", ms, err)
		}
		matched, ok := matchMove(g.Board.GenerateLegalMoves(), m)
		if !ok {
			return fmt.Errorf("illegal move %s in loaded game", ms)
		}
		g.PlayMove(matched)
	}
	return nil
}

// PlayMove applies a legal move and updates derived state. Caller must
// have already verified legality (see MatchMove).
func (g *Game) PlayMove(m Move) {
	u := g.Board.MakeMove(m)
	g.History = append(g.History, m.String())
	mv := m
	g.LastMove = &mv
	g.UndoStack = append(g.UndoStack, u)
	g.TouchedSq = NoSquare
	// Pawn moves and captures reset the halfmove clock and make older
	// positions unreachable, so the repetition counter starts fresh.
	if g.Board.HalfmoveClock == 0 {
		g.posCount = map[string]int{}
	}
	if g.posCount == nil {
		g.posCount = map[string]int{}
	}
	g.posCount[positionKey(g.Board)]++
}

// Undo reverts the most recent move; returns false if there is none.
// Repetition state is rebuilt from StartFEN to handle clock-reset events
// in the unwound segment correctly.
func (g *Game) Undo() bool {
	n := len(g.UndoStack)
	if n == 0 {
		return false
	}
	g.Board.UnmakeMove(g.UndoStack[n-1])
	g.History = g.History[:n-1]
	g.UndoStack = g.UndoStack[:n-1]
	g.TouchedSq = NoSquare
	g.TouchLost = false
	if len(g.UndoStack) > 0 {
		mv := g.UndoStack[len(g.UndoStack)-1].Move
		g.LastMove = &mv
	} else {
		g.LastMove = nil
	}
	g.rebuildPosCount()
	return true
}

func (g *Game) rebuildPosCount() {
	b, err := ParseFEN(g.StartFEN)
	if err != nil {
		g.posCount = map[string]int{positionKey(g.Board): 1}
		return
	}
	g.posCount = map[string]int{positionKey(b): 1}
	for _, u := range g.UndoStack {
		b.MakeMove(u.Move)
		if b.HalfmoveClock == 0 {
			g.posCount = map[string]int{}
		}
		g.posCount[positionKey(b)]++
	}
}

// Touch implements the touch-move rule. Idempotent on engine turns and
// when the game is already over; it is the caller's responsibility to
// only invoke this on TouchMove-enabled human turns.
func (g *Game) Touch(sq int) {
	if g.TouchLost || g.EngineToMove() || g.TouchedSq != NoSquare {
		return
	}
	p := g.Board.Squares[sq]
	if p.IsEmpty() || p.Color() != g.Board.SideToMove {
		return
	}
	for _, m := range g.Board.GenerateLegalMoves() {
		if m.From == sq {
			g.TouchedSq = sq
			return
		}
	}
	// Touched a piece that has no legal move — instant loss.
	g.TouchLost = true
}

// EngineToMove reports whether the side currently to move is the engine.
func (g *Game) EngineToMove() bool {
	if g.Board.SideToMove == White {
		return g.EngineWhite
	}
	return g.EngineBlack
}

// IsHuman reports whether color c is played by a human.
func (g *Game) IsHuman(c Color) bool {
	if c == White {
		return !g.EngineWhite
	}
	return !g.EngineBlack
}

// Status returns the current game outcome.
func (g *Game) Status() GameStatus {
	if g.TouchLost {
		return StatusTouchLost
	}
	legal := g.Board.GenerateLegalMoves()
	if len(legal) == 0 {
		if g.Board.InCheck(g.Board.SideToMove) {
			return StatusCheckmate
		}
		return StatusStalemate
	}
	if g.posCount[positionKey(g.Board)] >= 3 {
		return StatusDrawRepetition
	}
	if g.InsufficientMaterial() {
		return StatusDrawInsufficient
	}
	if g.Board.HalfmoveClock >= 100 {
		return StatusDraw50
	}
	return StatusOngoing
}

// PlayerAt returns the color that played UndoStack[i].
func (g *Game) PlayerAt(i int) Color {
	n := len(g.UndoStack)
	turn := g.Board.SideToMove // side to move *after* the last move
	// Player of UndoStack[i] = side that was to move just before move i,
	// = opposite of side-to-move just after move i. Side-to-move alternates,
	// so it differs from `turn` by (n-1-i) flips.
	if (n-1-i)%2 == 0 {
		return turn.Opp()
	}
	return turn
}

// LastHumanMoveIndex finds the most recent move played by a human; -1
// if none. In human-vs-engine games this lets /api/assess skip engine
// replies and grade the user's last decision.
func (g *Game) LastHumanMoveIndex() int {
	for i := len(g.UndoStack) - 1; i >= 0; i-- {
		if g.IsHuman(g.PlayerAt(i)) {
			return i
		}
	}
	return -1
}

// InsufficientMaterial recognises the standard FIDE draws by material:
// K vs K, K + (B or N) vs K, and K + B vs K + B with same-square-color
// bishops. Other endgames (KNN vs K, etc.) are *technically* not forced
// wins but FIDE counts them as live games.
func (g *Game) InsufficientMaterial() bool {
	var counts [2][7]int
	bishopColor := [2]int{-1, -1}
	for sq := 0; sq < 128; sq++ {
		if !OnBoard(sq) {
			continue
		}
		p := g.Board.Squares[sq]
		if p.IsEmpty() {
			continue
		}
		counts[p.Color()][p.Type()]++
		if p.Type() == Bishop {
			bishopColor[p.Color()] = (FileOf(sq) + RankOf(sq)) % 2
		}
	}
	for c := 0; c < 2; c++ {
		if counts[c][Pawn] > 0 || counts[c][Rook] > 0 || counts[c][Queen] > 0 {
			return false
		}
	}
	nW := counts[White][Knight] + counts[White][Bishop]
	nB := counts[Black][Knight] + counts[Black][Bishop]
	if nW == 0 && nB == 0 {
		return true
	}
	if (nW == 1 && nB == 0) || (nB == 1 && nW == 0) {
		return true
	}
	if counts[White][Bishop] == 1 && counts[Black][Bishop] == 1 &&
		counts[White][Knight] == 0 && counts[Black][Knight] == 0 &&
		bishopColor[White] == bishopColor[Black] {
		return true
	}
	return false
}

// positionKey is the FEN with halfmove and fullmove counters stripped —
// the canonical key for threefold-repetition equality.
func positionKey(b *Board) string {
	fields := strings.Fields(b.FEN())
	if len(fields) < 4 {
		return b.FEN()
	}
	return strings.Join(fields[:4], " ")
}

// matchMove finds the legal move equal to m (by from/to/promo).
func matchMove(legal []Move, m Move) (Move, bool) {
	for _, lm := range legal {
		if lm.Equal(m) {
			return lm, true
		}
	}
	return Move{}, false
}

// classifyAssessment returns a human label for a move, given the engine's
// best score and the user's resulting score (both in the user's POV) and
// the centipawn loss between them.
func classifyAssessment(played, best Move, cpLoss, bestScore, userScore int) string {
	if cpLoss <= -50 {
		return "Brilliant" // user found something better than the engine's pick
	}
	if played.Equal(best) {
		return "Best"
	}
	// Walking from a non-mated position into a forced loss is always a Blunder.
	if userScore <= -MateInMaxPly && bestScore > -MateInMaxPly {
		return "Blunder"
	}
	switch {
	case cpLoss <= 15:
		return "Excellent"
	case cpLoss <= 50:
		return "Good"
	case cpLoss <= 100:
		return "Inaccuracy"
	case cpLoss <= 250:
		return "Mistake"
	default:
		return "Blunder"
	}
}
