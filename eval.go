package main

// Material values in centipawns.
var pieceValue = [7]int{
	0,     // Empty
	100,   // Pawn
	320,   // Knight
	330,   // Bishop
	500,   // Rook
	900,   // Queen
	20000, // King (used for mate scoring; not added to material count in eval)
}

// Piece-square tables, from White's POV. Index 0 = a1, index 56 = a8.
// For Black, index via sq64 ^ 56.
var pstPawn = [64]int{
	0, 0, 0, 0, 0, 0, 0, 0,
	5, 10, 10, -20, -20, 10, 10, 5,
	5, -5, -10, 0, 0, -10, -5, 5,
	0, 0, 0, 20, 20, 0, 0, 0,
	5, 5, 10, 25, 25, 10, 5, 5,
	10, 10, 20, 30, 30, 20, 10, 10,
	50, 50, 50, 50, 50, 50, 50, 50,
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

var pstKing = [64]int{
	20, 30, 10, 0, 0, 10, 30, 20,
	20, 20, 0, 0, 0, 0, 20, 20,
	-10, -20, -20, -20, -20, -20, -20, -10,
	-20, -30, -30, -40, -40, -30, -30, -20,
	-30, -40, -40, -50, -50, -40, -40, -30,
	-30, -40, -40, -50, -50, -40, -40, -30,
	-30, -40, -40, -50, -50, -40, -40, -30,
	-30, -40, -40, -50, -50, -40, -40, -30,
}

func sq64(sq int) int { return (RankOf(sq) << 3) | FileOf(sq) }

func pstScore(t PieceType, c Color, sq int) int {
	idx := sq64(sq)
	if c == Black {
		idx ^= 56
	}
	switch t {
	case Pawn:
		return pstPawn[idx]
	case Knight:
		return pstKnight[idx]
	case Bishop:
		return pstBishop[idx]
	case Rook:
		return pstRook[idx]
	case Queen:
		return pstQueen[idx]
	case King:
		return pstKing[idx]
	}
	return 0
}

// Per-rank bonus for a passed pawn, indexed by "ranks past the home rank"
// (1..6 are the only live values; 0 and 7 don't apply to live pawns).
var passedPawnBonus = [8]int{0, 0, 5, 15, 30, 50, 80, 0}

// Evaluate returns a score in centipawns from the side-to-move's POV.
// Material + piece-square tables, plus structural terms: bishop pair,
// doubled/isolated/passed pawns, rook on open/semi-open file, and a
// pawn-shield king-safety penalty for kings that stayed near home.
func (b *Board) Evaluate() int {
	score := 0
	var whitePawnFiles, blackPawnFiles [8]int
	var whiteMinRank, blackMaxRank [8]int
	for f := 0; f < 8; f++ {
		whiteMinRank[f] = 8 // sentinel: no white pawn on file
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
		v := pieceValue[t] + pstScore(t, p.Color(), sq)
		if p.Color() == White {
			score += v
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
			score -= v
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

	// Bishop pair: two bishops cover both colors and coordinate well.
	if whiteBishops >= 2 {
		score += 30
	}
	if blackBishops >= 2 {
		score -= 30
	}

	// Pawn structure: doubled (per extra pawn on a file) and isolated
	// (pawn whose adjacent files are empty, weak because it can never be
	// defended by another pawn).
	for f := 0; f < 8; f++ {
		if whitePawnFiles[f] > 1 {
			score -= 15 * (whitePawnFiles[f] - 1)
		}
		if blackPawnFiles[f] > 1 {
			score += 15 * (blackPawnFiles[f] - 1)
		}
		leftW, rightW := 0, 0
		if f > 0 {
			leftW = whitePawnFiles[f-1]
		}
		if f < 7 {
			rightW = whitePawnFiles[f+1]
		}
		if whitePawnFiles[f] > 0 && leftW == 0 && rightW == 0 {
			score -= 15 * whitePawnFiles[f]
		}
		leftB, rightB := 0, 0
		if f > 0 {
			leftB = blackPawnFiles[f-1]
		}
		if f < 7 {
			rightB = blackPawnFiles[f+1]
		}
		if blackPawnFiles[f] > 0 && leftB == 0 && rightB == 0 {
			score += 15 * blackPawnFiles[f]
		}
	}

	// Second pass for terms that need the pawn maps: passed pawns and
	// rook activity on open/semi-open files.
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
					score += passedPawnBonus[r]
				}
			} else {
				passed := whiteMinRank[f] >= r &&
					(f == 0 || whiteMinRank[f-1] >= r) &&
					(f == 7 || whiteMinRank[f+1] >= r)
				if passed {
					score -= passedPawnBonus[7-r]
				}
			}
		case Rook:
			if p.Color() == White {
				if whitePawnFiles[f] == 0 {
					if blackPawnFiles[f] == 0 {
						score += 20 // fully open
					} else {
						score += 10 // semi-open
					}
				}
			} else {
				if blackPawnFiles[f] == 0 {
					if whitePawnFiles[f] == 0 {
						score -= 20
					} else {
						score -= 10
					}
				}
			}
		}
	}

	// King safety: when the king has stayed near home (back two ranks),
	// penalize missing pawn shield on the king's file and adjacent files.
	// Crude but cheap, and big enough to discourage opening pawn moves
	// in front of a castled king.
	wkSq := b.KingSquare[White]
	if RankOf(wkSq) <= 1 {
		wf := FileOf(wkSq)
		for df := -1; df <= 1; df++ {
			f := wf + df
			if f < 0 || f > 7 {
				continue
			}
			if whitePawnFiles[f] == 0 {
				score -= 12
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
				score += 12
			}
		}
	}

	if b.SideToMove == Black {
		score = -score
	}
	return score
}
