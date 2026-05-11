package main

// Material values in centipawns. Same in middlegame and endgame for now;
// PST tapering carries most of the phase-dependent scoring.
var pieceValue = [7]int{
	0,     // Empty
	100,   // Pawn
	320,   // Knight
	330,   // Bishop
	500,   // Rook
	900,   // Queen
	20000, // King (used for mate scoring; not added to material count in eval)
}

// Game-phase weight per piece type. Sum starts at 24 (4N + 4B + 4R*2 +
// 2Q*4) and shrinks toward 0 as pieces come off, driving interpolation
// between MG and EG PSTs in Evaluate.
const PhaseMax = 24

var phaseWeight = [7]int{0, 0, 1, 1, 2, 4, 0}

// Middlegame piece-square tables, from White's POV. Index 0 = a1, 56 = a8.
// For Black, index via sq64 ^ 56.
var pstPawnMG = [64]int{
	0, 0, 0, 0, 0, 0, 0, 0,
	5, 10, 10, -20, -20, 10, 10, 5,
	5, -5, -10, 0, 0, -10, -5, 5,
	0, 0, 0, 20, 20, 0, 0, 0,
	5, 5, 10, 25, 25, 10, 5, 5,
	10, 10, 20, 30, 30, 20, 10, 10,
	50, 50, 50, 50, 50, 50, 50, 50,
	0, 0, 0, 0, 0, 0, 0, 0,
}

// Endgame pawn table: every advance matters; near-promotion is huge.
var pstPawnEG = [64]int{
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	5, 5, 5, 5, 5, 5, 5, 5,
	10, 10, 10, 10, 10, 10, 10, 10,
	25, 25, 25, 25, 25, 25, 25, 25,
	50, 50, 50, 50, 50, 50, 50, 50,
	100, 100, 100, 100, 100, 100, 100, 100,
	0, 0, 0, 0, 0, 0, 0, 0,
}

var pstKnight = [64]int{
	-50, -40, -30, -30, -30, -30, -40, -50,
	-40, -20, 0, 5, 5, 0, -20, -40,
	-30, 5, 10, 15, 15, 10, 5, -30,
	-30, 0, 15, 20, 20, 15, 0, -30,
	-30, 5, 15, 20, 20, 15, 5, -30,
	-30, 0, 10, 15, 15, 10, 0, -30,
	-40, -20, 0, 0, 0, 0, -20, -40,
	-50, -40, -30, -30, -30, -30, -40, -50,
}

var pstBishop = [64]int{
	-20, -10, -10, -10, -10, -10, -10, -20,
	-10, 5, 0, 0, 0, 0, 5, -10,
	-10, 10, 10, 10, 10, 10, 10, -10,
	-10, 0, 10, 10, 10, 10, 0, -10,
	-10, 5, 5, 10, 10, 5, 5, -10,
	-10, 0, 5, 10, 10, 5, 0, -10,
	-10, 0, 0, 0, 0, 0, 0, -10,
	-20, -10, -10, -10, -10, -10, -10, -20,
}

var pstRook = [64]int{
	0, 0, 0, 5, 5, 0, 0, 0,
	-5, 0, 0, 0, 0, 0, 0, -5,
	-5, 0, 0, 0, 0, 0, 0, -5,
	-5, 0, 0, 0, 0, 0, 0, -5,
	-5, 0, 0, 0, 0, 0, 0, -5,
	-5, 0, 0, 0, 0, 0, 0, -5,
	5, 10, 10, 10, 10, 10, 10, 5,
	0, 0, 0, 0, 0, 0, 0, 0,
}

var pstQueen = [64]int{
	-20, -10, -10, -5, -5, -10, -10, -20,
	-10, 0, 5, 0, 0, 0, 0, -10,
	-10, 5, 5, 5, 5, 5, 0, -10,
	0, 0, 5, 5, 5, 5, 0, -5,
	-5, 0, 5, 5, 5, 5, 0, -5,
	-10, 0, 5, 5, 5, 5, 0, -10,
	-10, 0, 0, 0, 0, 0, 0, -10,
	-20, -10, -10, -5, -5, -10, -10, -20,
}

// Middlegame king PST: tucked into the corner behind a pawn shield.
var pstKingMG = [64]int{
	20, 30, 10, 0, 0, 10, 30, 20,
	20, 20, 0, 0, 0, 0, 20, 20,
	-10, -20, -20, -20, -20, -20, -20, -10,
	-20, -30, -30, -40, -40, -30, -30, -20,
	-30, -40, -40, -50, -50, -40, -40, -30,
	-30, -40, -40, -50, -50, -40, -40, -30,
	-30, -40, -40, -50, -50, -40, -40, -30,
	-30, -40, -40, -50, -50, -40, -40, -30,
}

// Endgame king PST: centralize. The king becomes a fighting piece once
// queens and most minors are gone.
var pstKingEG = [64]int{
	-50, -40, -30, -20, -20, -30, -40, -50,
	-30, -20, -10, 0, 0, -10, -20, -30,
	-30, -10, 20, 30, 30, 20, -10, -30,
	-30, -10, 30, 40, 40, 30, -10, -30,
	-30, -10, 30, 40, 40, 30, -10, -30,
	-30, -10, 20, 30, 30, 20, -10, -30,
	-30, -30, 0, 0, 0, 0, -30, -30,
	-50, -30, -30, -30, -30, -30, -30, -50,
}

func sq64(sq int) int { return (RankOf(sq) << 3) | FileOf(sq) }

// pstScores returns (middlegame, endgame) PST contributions for a piece
// of type t and color c at square sq. For pieces whose phase-dependent
// behavior is small (knight/bishop/rook/queen) MG and EG are equal.
func pstScores(t PieceType, c Color, sq int) (int, int) {
	idx := sq64(sq)
	if c == Black {
		idx ^= 56
	}
	switch t {
	case Pawn:
		return pstPawnMG[idx], pstPawnEG[idx]
	case Knight:
		return pstKnight[idx], pstKnight[idx]
	case Bishop:
		return pstBishop[idx], pstBishop[idx]
	case Rook:
		return pstRook[idx], pstRook[idx]
	case Queen:
		return pstQueen[idx], pstQueen[idx]
	case King:
		return pstKingMG[idx], pstKingEG[idx]
	}
	return 0, 0
}

// Per-rank passed-pawn bonus, indexed by ranks past home (1..6 are the
// only live values). Endgame values are larger because a passer's
// promotion threat dominates without pieces to blockade it.
var passedPawnMG = [8]int{0, 0, 5, 15, 30, 50, 80, 0}
var passedPawnEG = [8]int{0, 0, 10, 25, 45, 75, 130, 0}

// Evaluate returns a tapered score in centipawns from the side-to-move's
// POV. The position is scored twice — middlegame and endgame — and then
// blended by a game-phase factor derived from remaining non-pawn material.
func (b *Board) Evaluate() int {
	mg, eg := 0, 0
	phase := 0

	var whitePawnFiles, blackPawnFiles [8]int
	var whiteMinRank, blackMaxRank [8]int
	for f := 0; f < 8; f++ {
		whiteMinRank[f] = 8
		blackMaxRank[f] = -1
	}
	whiteBishops, blackBishops := 0, 0

	for sq := 0; sq < 128; sq++ {
		if !OnBoard(sq) {
			continue
		}
		p := b.Squares[sq]
		if p.IsEmpty() {
			continue
		}
		t := p.Type()
		c := p.Color()
		phase += phaseWeight[t]
		mat := pieceValue[t]
		mgPst, egPst := pstScores(t, c, sq)
		mgVal := mat + mgPst
		egVal := mat + egPst
		if c == White {
			mg += mgVal
			eg += egVal
			switch t {
			case Pawn:
				f, r := FileOf(sq), RankOf(sq)
				whitePawnFiles[f]++
				if r < whiteMinRank[f] {
					whiteMinRank[f] = r
				}
			case Bishop:
				whiteBishops++
			}
		} else {
			mg -= mgVal
			eg -= egVal
			switch t {
			case Pawn:
				f, r := FileOf(sq), RankOf(sq)
				blackPawnFiles[f]++
				if r > blackMaxRank[f] {
					blackMaxRank[f] = r
				}
			case Bishop:
				blackBishops++
			}
		}
	}
	if phase > PhaseMax {
		phase = PhaseMax
	}

	// Bishop pair: more valuable in open endgames where bishops can reach
	// long diagonals unobstructed.
	if whiteBishops >= 2 {
		mg += 30
		eg += 50
	}
	if blackBishops >= 2 {
		mg -= 30
		eg -= 50
	}

	// Pawn structure: doubled and isolated pawns are weak in both phases,
	// but the penalty bites harder in the endgame where pawn weaknesses
	// become attack targets.
	for f := 0; f < 8; f++ {
		if whitePawnFiles[f] > 1 {
			extra := whitePawnFiles[f] - 1
			mg -= 10 * extra
			eg -= 20 * extra
		}
		if blackPawnFiles[f] > 1 {
			extra := blackPawnFiles[f] - 1
			mg += 10 * extra
			eg += 20 * extra
		}
		leftW, rightW := 0, 0
		if f > 0 {
			leftW = whitePawnFiles[f-1]
		}
		if f < 7 {
			rightW = whitePawnFiles[f+1]
		}
		if whitePawnFiles[f] > 0 && leftW == 0 && rightW == 0 {
			mg -= 10 * whitePawnFiles[f]
			eg -= 20 * whitePawnFiles[f]
		}
		leftB, rightB := 0, 0
		if f > 0 {
			leftB = blackPawnFiles[f-1]
		}
		if f < 7 {
			rightB = blackPawnFiles[f+1]
		}
		if blackPawnFiles[f] > 0 && leftB == 0 && rightB == 0 {
			mg += 10 * blackPawnFiles[f]
			eg += 20 * blackPawnFiles[f]
		}
	}

	// Second pass for terms that need the pawn maps.
	for sq := 0; sq < 128; sq++ {
		if !OnBoard(sq) {
			continue
		}
		p := b.Squares[sq]
		if p.IsEmpty() {
			continue
		}
		t := p.Type()
		f := FileOf(sq)
		switch t {
		case Pawn:
			r := RankOf(sq)
			if p.Color() == White {
				passed := blackMaxRank[f] <= r &&
					(f == 0 || blackMaxRank[f-1] <= r) &&
					(f == 7 || blackMaxRank[f+1] <= r)
				if passed {
					mg += passedPawnMG[r]
					eg += passedPawnEG[r]
				}
			} else {
				passed := whiteMinRank[f] >= r &&
					(f == 0 || whiteMinRank[f-1] >= r) &&
					(f == 7 || whiteMinRank[f+1] >= r)
				if passed {
					mg -= passedPawnMG[7-r]
					eg -= passedPawnEG[7-r]
				}
			}
		case Rook:
			if p.Color() == White {
				if whitePawnFiles[f] == 0 {
					if blackPawnFiles[f] == 0 {
						mg += 20
						eg += 15
					} else {
						mg += 10
						eg += 5
					}
				}
			} else {
				if blackPawnFiles[f] == 0 {
					if whitePawnFiles[f] == 0 {
						mg -= 20
						eg -= 15
					} else {
						mg -= 10
						eg -= 5
					}
				}
			}
		}
	}

	// King safety (MG only). In the endgame the king should be an active
	// piece, so the EG king PST already pulls it forward — no shield
	// penalty applies there.
	wkSq := b.KingSquare[White]
	if RankOf(wkSq) <= 1 {
		wf := FileOf(wkSq)
		for df := -1; df <= 1; df++ {
			f := wf + df
			if f < 0 || f > 7 {
				continue
			}
			if whitePawnFiles[f] == 0 {
				mg -= 18
			}
		}
	}
	bkSq := b.KingSquare[Black]
	if RankOf(bkSq) >= 6 {
		bf := FileOf(bkSq)
		for df := -1; df <= 1; df++ {
			f := bf + df
			if f < 0 || f > 7 {
				continue
			}
			if blackPawnFiles[f] == 0 {
				mg += 18
			}
		}
	}

	score := (mg*phase + eg*(PhaseMax-phase)) / PhaseMax
	if b.SideToMove == Black {
		score = -score
	}
	return score
}
