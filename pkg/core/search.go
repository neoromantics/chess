package core

import (
	"sort"
	"sync/atomic"
	"time"
)

// SearchLimits defines the stopping conditions and context for a search.
type SearchLimits struct {
	MaxDepth int
	Nodes    int64
	MoveTime time.Duration
	Infinite bool

	// Repetition history (hash -> visit count) for the current game line.
	// In-search repetitions (the current search line) are handled separately
	// via a bitset or small map in the Searcher.
	History map[uint64]int
	// TT, if non-nil, is reused across calls (preserves knowledge between
	// moves). If nil, IterativeDeepening allocates a small per-search table.
	TT *TT
}

type SearchResult struct {
	BestMove Move
	Score    int
	Depth    int
	Nodes    int64
	PV       []Move
	Elapsed  time.Duration
}

func (r SearchResult) PVStrings() []string {
	var s []string
	for _, m := range r.PV {
		s = append(s, m.String())
	}
	return s
}

type Searcher struct {
	board       *Board
	stopFlag    *atomic.Bool
	deadline    time.Time
	hasDeadline bool

	history map[uint64]int // game history (from limits)
	path    map[uint64]int // search history (current line)
	nodes   int64

	tt *TT
}

func NewSearcher(b *Board, stop *atomic.Bool, deadline time.Time, hasDeadline bool, history map[uint64]int, tt *TT) *Searcher {
	return &Searcher{
		board:       b,
		stopFlag:    stop,
		deadline:    deadline,
		hasDeadline: hasDeadline,
		history:     history,
		path:        make(map[uint64]int),
		tt:          tt,
	}
}

const (
	MateScore      = 30000
	MateInMaxPly   = MateScore - 256
	InfiniteWindow = 1000000
)

// IterativeDeepening is the main entry point for the engine. It performs
// time-limited search starting at depth 1 and increasing until the limit
// is hit or a mate is found. infoCallback, if non-nil, receives intermediate
// results at the completion of each depth.
func (b *Board) IterativeDeepening(limits SearchLimits, stop *atomic.Bool, infoCallback func(SearchResult)) SearchResult {
	tt := limits.TT
	if tt == nil {
		tt = NewTT(8) // 8MB scratch table
	}
	deadline := time.Now().Add(limits.MoveTime)
	s := NewSearcher(b, stop, deadline, limits.MoveTime > 0, limits.History, tt)

	var lastRes SearchResult
	start := time.Now()

	for d := 1; d <= 100; d++ {
		if limits.MaxDepth > 0 && d > limits.MaxDepth {
			break
		}
		if s.shouldStop() {
			break
		}

		// Aspiration windows or simple root search
		score := s.search(-InfiniteWindow, InfiniteWindow, d, 0)

		if s.shouldStop() && d > 1 {
			break
		}

		res := SearchResult{
			Depth:   d,
			Score:   score,
			Nodes:   s.nodes,
			Elapsed: time.Since(start),
		}

		// Extract PV from TT
		res.PV = tt.ExtractPV(b, d)
		if len(res.PV) > 0 {
			res.BestMove = res.PV[0]
		}

		lastRes = res
		if infoCallback != nil {
			infoCallback(res)
		}

		// Early exit if mate found or time is low
		if score > MateInMaxPly || score < -MateInMaxPly {
			break
		}
		if s.hasDeadline && time.Since(start) > limits.MoveTime/2 {
			// If we've already spent half our time, don't start a deeper
			// iteration that we're unlikely to finish.
			break
		}
	}
	return lastRes
}

func (s *Searcher) shouldStop() bool {
	if s.stopFlag.Load() {
		return true
	}
	if s.hasDeadline && (s.nodes&2047 == 0) && time.Now().After(s.deadline) {
		s.stopFlag.Store(true)
		return true
	}
	return false
}

// search is a standard Alpha-Beta minimax with PVS, Move Ordering, TT,
// and Quiescence.
func (s *Searcher) search(alpha, beta, depth, ply int) int {
	s.nodes++
	if s.shouldStop() {
		return 0
	}

	b := s.board

	// Draw-by-rule short circuits. The repetition check is only meaningful
	// when reversible moves have been played since the last clock reset
	// (clock 0 means an irreversible move just happened, so older positions
	// are unreachable).
	if ply > 0 {
		if b.HalfmoveClock >= 100 {
			return 0 // 50-move rule
		}
		if b.HalfmoveClock > 0 {
			key := PositionKey(b)
			// Treat a 2nd visit on the search line, or a visit that would be
			// the 3rd in the actual game, as a draw the engine can play
			// for or against on cp grounds.
			if s.path[key] > 0 || s.history[key] >= 2 {
				return 0
			}
			s.path[key]++
			defer func() {
				s.path[key]--
			}()
		}
		if b.InsufficientMaterial() {
			return 0
		}
	}

	if depth <= 0 {
		return s.quiesce(alpha, beta)
	}

	// TT Lookup
	ttEntry, ttHit := s.tt.Probe(b.Hash)
	if ttHit && int(ttEntry.Depth) >= depth {
		score := scoreFromTT(ttEntry.Score, ply)
		if ttEntry.Flag == ttExact {
			return score
		}
		if ttEntry.Flag == ttLowerBound && score >= beta {
			return score
		}
		if ttEntry.Flag == ttUpperBound && score <= alpha {
			return score
		}
	}

	moves := b.GenerateLegalMoves()
	if len(moves) == 0 {
		if b.InCheck(b.SideToMove) {
			return -MateScore + ply // checkmate
		}
		return 0 // stalemate
	}

	// Move ordering: PV move from TT first, then captures by SEE, then others.
	sortMoves(b, moves, ttEntry.Move)

	bestScore := -InfiniteWindow
	oldAlpha := alpha
	var bestMove Move

	for i, m := range moves {
		u := b.MakeMove(m)

		var score int
		if i == 0 {
			// Full window search for first move
			score = -s.search(-beta, -alpha, depth-1, ply+1)
		} else {
			// Null Window Search (PVS)
			score = -s.search(-alpha-1, -alpha, depth-1, ply+1)
			if score > alpha && score < beta {
				// Re-search with full window if PVS failed high
				score = -s.search(-beta, -alpha, depth-1, ply+1)
			}
		}

		b.UnmakeMove(u)

		if s.shouldStop() {
			return 0
		}

		if score > bestScore {
			bestScore = score
			bestMove = m
			if score > alpha {
				alpha = score
				if score >= beta {
					break // beta cutoff
				}
			}
		}
	}

	// Store result in TT
	flag := ttUpperBound
	if bestScore >= beta {
		flag = ttLowerBound
	} else if bestScore > oldAlpha {
		flag = ttExact
	}
	s.tt.Store(b.Hash, bestMove, bestScore, depth, ply, flag)

	return bestScore
}

// quiesce extends the search by following capture sequences until a
// quiet position is reached (prevents "horizon effect" blunders).
func (s *Searcher) quiesce(alpha, beta int) int {
	s.nodes++
	if s.shouldStop() {
		return 0
	}

	// Standing pat: if the current static evaluation is good enough to
	// cause a beta cutoff, we don't need to look at captures.
	standPat := s.board.Evaluate()
	if standPat >= beta {
		return beta
	}
	if standPat > alpha {
		alpha = standPat
	}

	moves := s.board.GenerateCaptures()
	// Order captures by MVV-LVA or SEE
	sortMoves(s.board, moves, Move{})

	for _, m := range moves {
		// Delta pruning or SEE pruning could go here.
		// For now, only prune if SEE says we lose material.
		if s.board.SEE(m) < 0 {
			continue
		}

		u := s.board.MakeMove(m)
		score := -s.quiesce(-beta, -alpha)
		s.board.UnmakeMove(u)

		if score >= beta {
			return beta
		}
		if score > alpha {
			alpha = score
		}
	}

	return alpha
}

func sortMoves(b *Board, moves []Move, pvMove Move) {
	// Simple move ordering:
	// 1. PV move (from TT)
	// 2. Captures (ordered by victim type)
	// 3. Others

	scoreFn := func(m Move) int {
		if m.Equal(pvMove) {
			return 1000000
		}
		if m.IsCapture(b) {
			// MVV-LVA: Most Valuable Victim - Least Valuable Attacker
			// Victim type is stored in the 0x88 square we're moving to.
			victim := b.Squares[m.To].Type()
			attacker := b.Squares[m.From].Type()
			return 10000 + int(victim)*10 - int(attacker)
		}
		return 0
	}

	sort.SliceStable(moves, func(i, j int) bool {
		return scoreFn(moves[i]) > scoreFn(moves[j])
	})
}
