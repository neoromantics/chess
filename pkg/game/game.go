package game

import (
	"fmt"

	"github.com/neoromantics/chess/pkg/core"
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
	Board      *core.Board
	StartFEN   string
	History    []string // long-algebraic, parallel to UndoStack and HistorySAN
	HistorySAN []string // standard algebraic ("e4", "Nf3", "O-O-O", "exd5+")
	LastMove   *core.Move
	UndoStack  []core.Undo

	EngineWhite bool
	EngineBlack bool

	// Tournament-style touch-move rule (opt-in).
	TouchMove bool
	TouchedSq int  // core.NoSquare if no piece is currently touched
	TouchLost bool // sticky once a touch-move loss is declared

	// Repetition counter keyed by Zobrist hash.
	posCount map[uint64]int
}

// NewGame returns a fresh game in the standard starting position.
func NewGame() *Game {
	g := &Game{TouchedSq: core.NoSquare}
	g.Reset()
	return g
}

// Reset returns the game to the standard starting position. Mode flags
// (engine sides, touch-move) are preserved.
func (g *Game) Reset() {
	g.Board = core.StartPosition()
	g.StartFEN = core.StartFEN
	g.History = nil
	g.HistorySAN = nil
	g.LastMove = nil
	g.UndoStack = nil
	g.TouchedSq = core.NoSquare
	g.TouchLost = false
	g.posCount = map[uint64]int{core.PositionKey(g.Board): 1}
}

// Load reinitialises from a starting FEN and replays moves. Engine-side
// flags come from the caller; touch-move preference is preserved.
func (g *Game) Load(startFEN string, moves []string, engineW, engineB bool) error {
	if startFEN == "" {
		startFEN = core.StartFEN
	}
	b, err := core.ParseFEN(startFEN)
	if err != nil {
		return fmt.Errorf("bad start FEN: %w", err)
	}
	g.Board = b
	g.StartFEN = startFEN
	g.History = nil
	g.HistorySAN = nil
	g.LastMove = nil
	g.UndoStack = nil
	g.TouchedSq = core.NoSquare
	g.TouchLost = false
	g.EngineWhite = engineW
	g.EngineBlack = engineB
	g.posCount = map[uint64]int{core.PositionKey(g.Board): 1}
	for _, ms := range moves {
		m, err := g.Board.ParseUCIMove(ms)
		if err != nil {
			return fmt.Errorf("bad move %s: %w", ms, err)
		}
		matched, ok := MatchMove(g.Board.GenerateLegalMoves(), m)
		if !ok {
			return fmt.Errorf("illegal move %s in loaded game", ms)
		}
		g.PlayMove(matched)
	}
	return nil
}

// PlayMove applies a legal move and updates derived state. Caller must
// have already verified legality (see MatchMove).
func (g *Game) PlayMove(m core.Move) {
	// SAN must be computed against the *pre-move* board (it inspects
	// disambiguation candidates and tentatively makes the move for +/#).
	san := core.MoveToSAN(g.Board, m)
	u := g.Board.MakeMove(m)
	g.History = append(g.History, m.String())
	g.HistorySAN = append(g.HistorySAN, san)
	mv := m
	g.LastMove = &mv
	g.UndoStack = append(g.UndoStack, u)
	g.TouchedSq = core.NoSquare
	// Pawn moves and captures reset the halfmove clock and make older
	// positions unreachable, so the repetition counter starts fresh.
	if g.Board.HalfmoveClock == 0 {
		g.posCount = map[uint64]int{}
	}
	if g.posCount == nil {
		g.posCount = map[uint64]int{}
	}
	g.posCount[core.PositionKey(g.Board)]++
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
	g.HistorySAN = g.HistorySAN[:n-1]
	g.UndoStack = g.UndoStack[:n-1]
	g.TouchedSq = core.NoSquare
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
	b, err := core.ParseFEN(g.StartFEN)
	if err != nil {
		g.posCount = map[uint64]int{core.PositionKey(g.Board): 1}
		return
	}
	g.posCount = map[uint64]int{core.PositionKey(b): 1}
	for _, u := range g.UndoStack {
		b.MakeMove(u.Move)
		if b.HalfmoveClock == 0 {
			g.posCount = map[uint64]int{}
		}
		g.posCount[core.PositionKey(b)]++
	}
}

// Touch implements the touch-move rule. Idempotent on engine turns and
// when the game is already over; it is the caller's responsibility to
// only invoke this on TouchMove-enabled human turns.
func (g *Game) Touch(sq int) {
	if g.TouchLost || g.EngineToMove() || g.TouchedSq != core.NoSquare {
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
	if g.Board.SideToMove == core.White {
		return g.EngineWhite
	}
	return g.EngineBlack
}

// IsHuman reports whether color c is played by a human.
func (g *Game) IsHuman(c core.Color) bool {
	if c == core.White {
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
	if g.posCount[core.PositionKey(g.Board)] >= 3 {
		return StatusDrawRepetition
	}
	if g.Board.InsufficientMaterial() {
		return StatusDrawInsufficient
	}
	if g.Board.HalfmoveClock >= 100 {
		return StatusDraw50
	}
	return StatusOngoing
}

// BoardsAroundMove returns snapshots of the position just before and just
// after UndoStack[idx] was played. The live g.Board is unchanged: the
// method temporarily unmakes back to idx, snapshots, makes the target
// move, snapshots, then replays the rest. Caller must ensure idx is in
// range [0, len(UndoStack)).
func (g *Game) BoardsAroundMove(idx int) (before, after core.Board) {
	n := len(g.UndoStack)
	for j := n - 1; j >= idx; j-- {
		g.Board.UnmakeMove(g.UndoStack[j])
	}
	before = *g.Board
	g.Board.MakeMove(g.UndoStack[idx].Move)
	after = *g.Board
	for j := idx + 1; j < n; j++ {
		g.Board.MakeMove(g.UndoStack[j].Move)
	}
	return before, after
}

// PlayerAt returns the color that played UndoStack[i].
func (g *Game) PlayerAt(i int) core.Color {
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

// ReplayFrame is one step of a recorded game, suitable for driving a
// step-through replay player.
type ReplayFrame struct {
	FEN  string `json:"fen"`
	SAN  string `json:"san,omitempty"`  // empty for the initial position
	From string `json:"from,omitempty"` // empty for the initial position
	To   string `json:"to,omitempty"`
}

// ReplayData returns the position after each ply, starting with the game's
// initial position. Length = len(History) + 1.
func (g *Game) ReplayData() []ReplayFrame {
	frames := []ReplayFrame{}
	b, err := core.ParseFEN(g.StartFEN)
	if err != nil {
		return frames
	}
	frames = append(frames, ReplayFrame{FEN: b.FEN()})
	for i, u := range g.UndoStack {
		b.MakeMove(u.Move)
		san := ""
		if i < len(g.HistorySAN) {
			san = g.HistorySAN[i]
		}
		frames = append(frames, ReplayFrame{
			FEN:  b.FEN(),
			SAN:  san,
			From: core.SquareName(u.Move.From),
			To:   core.SquareName(u.Move.To),
		})
	}
	return frames
}

// HistoryHash returns the current repetition state.
func (g *Game) HistoryHash() map[uint64]int {
	return g.posCount
}

// MatchMove finds the legal move equal to m (by from/to/promo).
func MatchMove(legal []core.Move, m core.Move) (core.Move, bool) {
	for _, lm := range legal {
		if lm.Equal(m) {
			return lm, true
		}
	}
	return core.Move{}, false
}

// ClassifyAssessment returns a human label for a move, given the engine's
// best score and the user's resulting score (both in the user's POV) and
// the centipawn loss between them.
func ClassifyAssessment(played, best core.Move, cpLoss, bestScore, userScore int) string {
	if cpLoss <= -50 {
		return "Brilliant" // user found something better than the engine's pick
	}
	if played.Equal(best) {
		return "Best"
	}
	// Walking from a non-mated position into a forced loss is always a Blunder.
	if userScore <= -core.MateInMaxPly && bestScore > -core.MateInMaxPly {
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

// CopyHistory helps pass game repetition state to the engine without holding the
// lock and without racing with subsequent PlayMove writes.
func CopyHistory(src map[uint64]int) map[uint64]int {
	dst := make(map[uint64]int, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
