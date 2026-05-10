package main

import (
	"sort"
	"sync/atomic"
	"time"
)

const (
	MateScore    = 30000
	MateInMaxPly = MateScore - 1000 // any score above this is a mate
	InfScore     = 32000
	MaxPly       = 64
)

type SearchLimits struct {
	MaxDepth int           // 0 = use MaxPly
	MoveTime time.Duration // 0 = no per-move bound
	Infinite bool
	// History is the count of how many times each prior position has been
	// reached in the actual game. Lets the engine treat moves that lead to
	// 3-fold-repetition as draws (score 0). nil = no awareness.
	History map[string]int
}

type SearchResult struct {
	BestMove Move
	Score    int
	Depth    int
	Nodes    int64
	PV       []Move
	Elapsed  time.Duration
}

type Searcher struct {
	board       *Board
	stopFlag    *atomic.Bool
	deadline    time.Time
	hasDeadline bool
	nodes       int64
	startTime   time.Time
	pv          [MaxPly + 1][MaxPly + 1]Move
	pvLen       [MaxPly + 1]int

	// Repetition awareness: history is the prior-game position counts
	// (read-only); path tracks positions visited in the current search line.
	history map[string]int
	path    map[string]int
}

func newSearcher(b *Board, stop *atomic.Bool, deadline time.Time, hasDeadline bool, history map[string]int) *Searcher {
	return &Searcher{
		board: b, stopFlag: stop, deadline: deadline, hasDeadline: hasDeadline,
		startTime: time.Now(),
		history:   history,
		path:      map[string]int{},
	}
}

// shouldStop is checked frequently inside the search.
func (s *Searcher) shouldStop() bool {
	if s.stopFlag != nil && s.stopFlag.Load() {
		return true
	}
	// Time check (cheap-ish; we throttle with the nodes mask).
	if s.hasDeadline && s.nodes&4095 == 0 && time.Now().After(s.deadline) {
		return true
	}
	return false
}

// IterativeDeepening drives the search depth-by-depth. info, if non-nil, is
// called after each completed iteration so the UCI layer can print info lines.
func (b *Board) IterativeDeepening(limits SearchLimits, stop *atomic.Bool, info func(SearchResult)) SearchResult {
	maxDepth := limits.MaxDepth
	if maxDepth <= 0 || maxDepth > MaxPly {
		maxDepth = MaxPly
	}
	var deadline time.Time
	hasDeadline := false
	if limits.MoveTime > 0 {
		deadline = time.Now().Add(limits.MoveTime)
		hasDeadline = true
	}

	s := newSearcher(b, stop, deadline, hasDeadline, limits.History)
	var best SearchResult
	// Seed best with the first legal move so we always return something legal.
	legal := b.GenerateLegalMoves()
	if len(legal) == 0 {
		return SearchResult{}
	}
	best.BestMove = legal[0]

	for depth := 1; depth <= maxDepth; depth++ {
		score := s.negamax(depth, 0, -InfScore, InfScore)
		if s.shouldStop() && depth > 1 {
			break
		}
		// Build PV for this depth.
		pv := make([]Move, s.pvLen[0])
		copy(pv, s.pv[0][:s.pvLen[0]])
		if len(pv) > 0 {
			best.BestMove = pv[0]
		}
		best.Score = score
		best.Depth = depth
		best.Nodes = s.nodes
		best.PV = pv
		best.Elapsed = time.Since(s.startTime)
		if info != nil {
			info(best)
		}
		// If we found a forced mate, no need to search deeper.
		if score >= MateInMaxPly || score <= -MateInMaxPly {
			break
		}
	}
	return best
}

func (s *Searcher) negamax(depth, ply int, alpha, beta int) int {
	if s.shouldStop() {
		return 0
	}
	s.nodes++
	s.pvLen[ply] = 0

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
			key := positionKey(b)
			// Treat a 2nd visit on the search line, or a visit that would be
			// the 3rd in the actual game, as a draw the engine can play
			// for or against on cp grounds.
			if s.path[key] > 0 || s.history[key] >= 2 {
				return 0
			}
			s.path[key]++
			defer func() {
				s.path[key]--
				if s.path[key] == 0 {
					delete(s.path, key)
				}
			}()
		}
	}

	if depth <= 0 {
		return s.quiesce(ply, alpha, beta)
	}

	us := b.SideToMove
	inCheck := b.InCheck(us)

	moves := make([]Move, 0, 48)
	b.GeneratePseudoLegalMoves(&moves)
	s.orderMoves(moves)

	legalCount := 0
	bestScore := -InfScore

	for _, m := range moves {
		u := b.MakeMove(m)
		if b.SquareAttacked(b.KingSquare[us], us.Opp()) {
			b.UnmakeMove(u)
			continue
		}
		legalCount++
		score := -s.negamax(depth-1, ply+1, -beta, -alpha)
		b.UnmakeMove(u)

		if s.shouldStop() {
			return 0
		}

		if score > bestScore {
			bestScore = score
			if score > alpha {
				alpha = score
				// Update PV.
				s.pv[ply][0] = m
				copy(s.pv[ply][1:], s.pv[ply+1][:s.pvLen[ply+1]])
				s.pvLen[ply] = s.pvLen[ply+1] + 1
			}
			if alpha >= beta {
				break // beta cutoff
			}
		}
	}

	if legalCount == 0 {
		if inCheck {
			return -MateScore + ply // checkmate; prefer faster mates
		}
		return 0 // stalemate
	}
	return bestScore
}

func (s *Searcher) quiesce(ply int, alpha, beta int) int {
	if s.shouldStop() {
		return 0
	}
	s.nodes++
	stand := s.board.Evaluate()
	if stand >= beta {
		return stand
	}
	if stand > alpha {
		alpha = stand
	}
	if ply >= MaxPly {
		return stand
	}

	b := s.board
	us := b.SideToMove

	moves := make([]Move, 0, 32)
	b.GeneratePseudoLegalMoves(&moves)
	// Filter to captures + promotions (en-passant counts as capture).
	captures := moves[:0]
	for _, m := range moves {
		isCapture := !b.Squares[m.To].IsEmpty() || m.Flag == FlagEP
		if isCapture || m.Promo != Empty {
			captures = append(captures, m)
		}
	}
	s.orderMoves(captures)

	for _, m := range captures {
		u := b.MakeMove(m)
		if b.SquareAttacked(b.KingSquare[us], us.Opp()) {
			b.UnmakeMove(u)
			continue
		}
		score := -s.quiesce(ply+1, -beta, -alpha)
		b.UnmakeMove(u)
		if s.shouldStop() {
			return 0
		}
		if score >= beta {
			return score
		}
		if score > alpha {
			alpha = score
		}
	}
	return alpha
}

// orderMoves sorts in-place: captures first by MVV-LVA, promotions get a big
// bonus, quiet moves last. This is plenty for a starter engine.
func (s *Searcher) orderMoves(moves []Move) {
	b := s.board
	scoreFn := func(m Move) int {
		score := 0
		victim := b.Squares[m.To]
		if !victim.IsEmpty() {
			attacker := b.Squares[m.From]
			score += 10*pieceValue[victim.Type()] - pieceValue[attacker.Type()]
		} else if m.Flag == FlagEP {
			score += 10 * pieceValue[Pawn]
		}
		if m.Promo != Empty {
			score += pieceValue[m.Promo]
		}
		return score
	}
	sort.SliceStable(moves, func(i, j int) bool {
		return scoreFn(moves[i]) > scoreFn(moves[j])
	})
}
