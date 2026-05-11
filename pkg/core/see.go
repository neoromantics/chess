package core

// Static Exchange Evaluation: simulate the capture sequence on a square
// using least-valuable-attacker swaps, alternately, and return the net
// material gain in centipawns from the perspective of the side to move.
//
// Used in qsearch to skip captures that lose material outright (huge
// node savings) and could equally drive capture ordering. X-rays are
// handled by re-scanning sliders each iteration with already-captured
// pieces masked out via the `removed` set.
func (b *Board) SEE(m Move) int {
	target := m.To
	side := b.SideToMove

	var captured PieceType
	if m.Flag == FlagEP {
		captured = Pawn
	} else if !b.Squares[target].IsEmpty() {
		captured = b.Squares[target].Type()
	} else {
		return 0
	}

	var removed [128]bool
	removed[m.From] = true
	if m.Flag == FlagEP {
		epSq := target
		if side == White {
			epSq -= 16
		} else {
			epSq += 16
		}
		removed[epSq] = true
	}

	var gain [32]int
	d := 0
	gain[0] = pieceValue[captured]

	onSquareType := b.Squares[m.From].Type()
	if m.Promo != Empty {
		// Pawn morphs into the promoted piece on the target square.
		gain[0] += pieceValue[m.Promo] - pieceValue[Pawn]
		onSquareType = m.Promo
	}

	side = side.Opp()

	for {
		from, attType := b.leastValuableAttacker(target, side, &removed)
		if attType == Empty {
			break
		}
		d++
		gain[d] = pieceValue[onSquareType] - gain[d-1]
		if max(-gain[d-1], gain[d]) < 0 {
			break
		}
		removed[from] = true
		onSquareType = attType
		side = side.Opp()
	}

	for d > 0 {
		if -gain[d] < gain[d-1] {
			gain[d-1] = -gain[d]
		}
		d--
	}
	return gain[0]
}

// leastValuableAttacker returns the source square and type of the cheapest
// piece of `side` attacking `target`, ignoring pieces marked in `removed`.
// Pawn → Knight → Bishop → Rook → Queen → King, with bishop/rook lookups
// also catching queens on those rays.
func (b *Board) leastValuableAttacker(target int, side Color, removed *[128]bool) (int, PieceType) {
	if side == White {
		for _, d := range [2]int{-NE, -NW} {
			ps := target + d
			if OnBoard(ps) && !removed[ps] {
				p := b.Squares[ps]
				if p.Type() == Pawn && p.Color() == White {
					return ps, Pawn
				}
			}
		}
	} else {
		for _, d := range [2]int{-SE, -SW} {
			ps := target + d
			if OnBoard(ps) && !removed[ps] {
				p := b.Squares[ps]
				if p.Type() == Pawn && p.Color() == Black {
					return ps, Pawn
				}
			}
		}
	}

	for _, d := range knightOffsets {
		ks := target + d
		if OnBoard(ks) && !removed[ks] {
			p := b.Squares[ks]
			if p.Type() == Knight && p.Color() == side {
				return ks, Knight
			}
		}
	}

	bestSq, bestType := -1, Empty
	for _, dir := range bishopRays {
		for s := target + dir; OnBoard(s); s += dir {
			if removed[s] {
				continue
			}
			p := b.Squares[s]
			if p.IsEmpty() {
				continue
			}
			if p.Color() == side && (p.Type() == Bishop || p.Type() == Queen) {
				if bestType == Empty || pieceValue[p.Type()] < pieceValue[bestType] {
					bestSq, bestType = s, p.Type()
				}
			}
			break
		}
	}
	if bestType == Bishop {
		return bestSq, Bishop
	}

	rBestSq, rBestType := -1, Empty
	for _, dir := range rookRays {
		for s := target + dir; OnBoard(s); s += dir {
			if removed[s] {
				continue
			}
			p := b.Squares[s]
			if p.IsEmpty() {
				continue
			}
			if p.Color() == side && (p.Type() == Rook || p.Type() == Queen) {
				if rBestType == Empty || pieceValue[p.Type()] < pieceValue[rBestType] {
					rBestSq, rBestType = s, p.Type()
				}
			}
			break
		}
	}
	if rBestType == Rook {
		return rBestSq, Rook
	}

	// Anything left on bishop/rook scans is a queen (if it had been a
	// bishop/rook it would already have returned). Pick whichever was found.
	if bestType != Empty {
		return bestSq, bestType
	}
	if rBestType != Empty {
		return rBestSq, rBestType
	}

	for _, d := range kingOffsets {
		ks := target + d
		if OnBoard(ks) && !removed[ks] {
			p := b.Squares[ks]
			if p.Type() == King && p.Color() == side {
				return ks, King
			}
		}
	}

	return -1, Empty
}
