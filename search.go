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
	history map[uint64]int
	path    map[uint64]int

	tt *TT

	// killers[ply] holds up to two non-capture moves that recently caused
	// a beta cutoff at this ply; orderMoves prefers them right after
	// captures so similar cutoffs happen earlier.
	killers [MaxPly + 1][2]Move

	// historyTable[color][from][to] accumulates depth² for every quiet move
	// that produced a beta cutoff. orderMoves uses it as a tiebreaker for
	// quiet moves so historically-strong moves are tried earlier. Boxed so
	// the Searcher value stays small.
	historyTable *[2][128][128]int
}

// hasNonPawnMaterial returns true if c has at least one knight, bishop,
// rook, or queen on the board. Null-move pruning is unsafe in king-and-
// pawn-only positions because zugzwang is common there.
func hasNonPawnMaterial(b *Board, c Color) bool {
	for sq := 0; sq < 128; sq++ {
		if !OnBoard(sq) {
			continue
		}
		p := b.Squares[sq]
		if p.IsEmpty() || p.Color() != c {
			continue
		}
		t := p.Type()
		if t != Pawn && t != King {
			return true
		}
	}
	return false
}

func newSearcher(b *Board, stop *atomic.Bool, deadline time.Time, hasDeadline bool, history map[uint64]int, tt *TT) *Searcher {
	if tt == nil {
		tt = NewTT(16) // 16 MB default per call when no shared TT supplied
	}
	return &Searcher{
		board: b, stopFlag: stop, deadline: deadline, hasDeadline: hasDeadline,
		startTime:    time.Now(),
		history:      history,
		path:         map[uint64]int{},
		tt:           tt,
		historyTable: &[2][128][128]int{},
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

	s := newSearcher(b, stop, deadline, hasDeadline, limits.History, limits.TT)
	var best SearchResult
	// Seed best with the first legal move so we always return something legal.
	legal := b.GenerateLegalMoves()
	if len(legal) == 0 {
		return SearchResult{}
	}
	best.BestMove = legal[0]

	for depth := 1; depth <= maxDepth; depth++ {
		// Aspiration windows: once we have a reliable score, search a narrow
		// band around it. On a fail-high or fail-low, widen and re-search at
		// the same depth — much cheaper on average than the (-inf, +inf)
		// full window because narrower bounds give more cutoffs.
		alpha, beta := -InfScore, InfScore
		if depth >= 5 && best.Score > -MateInMaxPly && best.Score < MateInMaxPly {
			alpha = best.Score - 50
			beta = best.Score + 50
		}
		delta := 100
		var score int
		for {
			score = s.negamax(depth, 0, alpha, beta)
			if s.shouldStop() {
				break
			}
			if score > alpha && score < beta {
				break
			}
			if score <= alpha {
				alpha -= delta
				if alpha < -InfScore {
					alpha = -InfScore
				}
			} else {
				beta += delta
				if beta > InfScore {
					beta = InfScore
				}
			}
			delta *= 2
		}
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

	// TT probe. At a non-PV node with sufficient depth we can return the
	// stored score directly. Even on a "soft" hit (depth too shallow),
	// the stored move feeds move ordering as a strong PV-style hint.
	var ttMove Move
	if entry, ok := s.tt.Probe(b.Hash); ok {
		ttMove = entry.Move
		if int(entry.Depth) >= depth && ply > 0 {
			score := scoreFromTT(entry.Score, ply)
			switch entry.Flag {
			case ttExact:
				return score
			case ttLowerBound:
				if score >= beta {
					return score
				}
			case ttUpperBound:
				if score <= alpha {
					return score
				}
			}
		}
	}

	if depth <= 0 {
		return s.quiesce(ply, alpha, beta)
	}

	us := b.SideToMove
	inCheck := b.InCheck(us)
	isPV := beta-alpha > 1

	// Reverse futility pruning (a.k.a. static null-move pruning). At shallow
	// depth in a non-PV node, if our static eval beats beta by a generous
	// margin even after a per-depth penalty, the position is winning enough
	// that we can skip the search. Skipped in check (eval is unreliable) and
	// in PV nodes (we want the exact score there).
	if !isPV && depth <= 4 && ply > 0 && !inCheck && beta < MateInMaxPly && beta > -MateInMaxPly {
		eval := b.Evaluate()
		margin := 80 * depth
		if eval-margin >= beta {
			return eval - margin
		}
	}

	// Null-move pruning. If we can pass the move and a depth-reduced
	// search still gives the opponent ≥ beta, the position is so good
	// that we don't need to search further. Skipped in check (illegal
	// to "pass" out of check) and in pawn-only endings (zugzwang risk).
	if depth >= 3 && ply > 0 && !inCheck && beta < MateInMaxPly && hasNonPawnMaterial(b, us) {
		nullU := b.MakeNullMove()
		const R = 2
		score := -s.negamax(depth-1-R, ply+1, -beta, -beta+1)
		b.UnmakeNullMove(nullU)
		if s.shouldStop() {
			return 0
		}
		if score >= beta {
			return beta
		}
	}

	moves := make([]Move, 0, 48)
	b.GeneratePseudoLegalMoves(&moves)
	s.orderMoves(moves, ttMove, ply)

	// Check extension: if we are in check, search children at unreduced depth
	// (newDepth = depth instead of depth-1). Avoids dropping into qsearch
	// while in check (qsearch only generates captures, so it would miss
	// quiet escapes) and adds tactical depth along forcing lines.
	ext := 0
	if inCheck {
		ext = 1
	}
	newDepth := depth - 1 + ext

	legalCount := 0
	bestScore := -InfScore
	var bestMove Move
	flag := ttUpperBound

	for _, m := range moves {
		u := b.MakeMove(m)
		if b.SquareAttacked(b.KingSquare[us], us.Opp()) {
			b.UnmakeMove(u)
			continue
		}
		legalCount++
		// LMR: after the first few moves, reduce depth for quiet, non-tactical
		// moves. If the reduced search beats alpha, re-search at full depth.
		// Captures, promotions, checks-given, and search nodes already in
		// check stay at full depth.
		gaveCheck := b.InCheck(b.SideToMove)
		reduction := 0
		if newDepth >= 3 && legalCount > 3 && !inCheck && !gaveCheck && u.Captured == 0 && m.Promo == Empty {
			reduction = 1
			if legalCount > 6 && newDepth >= 5 {
				reduction = 2
			}
		}

		// PVS: search the first move with the full window; later moves get
		// a (possibly reduced) null-window scout. Re-search at full depth
		// when LMR overshoots and at full window when the move turns out
		// to be PV.
		var score int
		if legalCount == 1 {
			score = -s.negamax(newDepth, ply+1, -beta, -alpha)
		} else {
			score = -s.negamax(newDepth-reduction, ply+1, -alpha-1, -alpha)
			if !s.shouldStop() && reduction > 0 && score > alpha {
				score = -s.negamax(newDepth, ply+1, -alpha-1, -alpha)
			}
			if !s.shouldStop() && score > alpha && score < beta {
				score = -s.negamax(newDepth, ply+1, -beta, -alpha)
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
				flag = ttExact
				// Update PV.
				s.pv[ply][0] = m
				copy(s.pv[ply][1:], s.pv[ply+1][:s.pvLen[ply+1]])
				s.pvLen[ply] = s.pvLen[ply+1] + 1
			}
			if alpha >= beta {
				flag = ttLowerBound
				// Quiet-move cutoff bookkeeping: store as killer at this ply
				// and accumulate history score so the move sorts earlier in
				// other branches that share the same from/to squares.
				if u.Captured == 0 && m.Promo == Empty {
					if !s.killers[ply][0].Equal(m) {
						s.killers[ply][1] = s.killers[ply][0]
						s.killers[ply][0] = m
					}
					s.historyTable[us][m.From][m.To] += depth * depth
				}
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

	s.tt.Store(b.Hash, bestMove, bestScore, depth, ply, flag)
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
	s.orderMoves(captures, Move{}, ply)

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

// orderMoves sorts in-place. Priority (highest first):
//  1. The TT-suggested move (best move from a previous search of this node)
//  2. Captures by MVV-LVA, promotions by promo piece value
//  3. Killer moves at this ply (non-capture cutoffs from sibling nodes)
//  4. Quiet moves ranked by history (depth² accumulated on past cutoffs)
func (s *Searcher) orderMoves(moves []Move, ttMove Move, ply int) {
	b := s.board
	hasTT := ttMove != (Move{})
	us := b.SideToMove
	k0, k1 := s.killers[ply][0], s.killers[ply][1]
	scoreFn := func(m Move) int {
		if hasTT && m.Equal(ttMove) {
			return 1_000_000
		}
		score := 0
		victim := b.Squares[m.To]
		if !victim.IsEmpty() {
			attacker := b.Squares[m.From]
			score += 100_000 + 10*pieceValue[victim.Type()] - pieceValue[attacker.Type()]
		} else if m.Flag == FlagEP {
			score += 100_000 + 10*pieceValue[Pawn]
		}
		if m.Promo != Empty {
			score += pieceValue[m.Promo]
		}
		// Quiet-move ranking: killers first, then history. Capped so
		// history can never overtake a killer or capture.
		if score == 0 {
			if (k0 != Move{}) && m.Equal(k0) {
				score = 9000
			} else if (k1 != Move{}) && m.Equal(k1) {
				score = 8000
			} else {
				h := s.historyTable[us][m.From][m.To]
				if h > 7000 {
					h = 7000
				}
				score = h
			}
		}
		return score
	}
	sort.SliceStable(moves, func(i, j int) bool {
		return scoreFn(moves[i]) > scoreFn(moves[j])
	})
}
