package core

// Direction offsets in the 0x88 layout.
const (
	N  = 16
	S  = -16
	E  = 1
	W  = -1
	NE = N + E
	NW = N + W
	SE = S + E
	SW = S + W
)

var (
	knightOffsets = [8]int{N + N + E, N + N + W, S + S + E, S + S + W, E + E + N, E + E + S, W + W + N, W + W + S}
	kingOffsets   = [8]int{N, S, E, W, NE, NW, SE, SW}
	bishopRays    = [4]int{NE, NW, SE, SW}
	rookRays      = [4]int{N, S, E, W}
	queenRays     = [8]int{N, S, E, W, NE, NW, SE, SW}
)

// SquareAttacked reports whether sq is attacked by any piece of `by` color.
func (b *Board) SquareAttacked(sq int, by Color) bool {
	// Pawn attacks: from sq's POV, look "backward" along the attacker's capture path.
	if by == White {
		// White pawns attack NE/NW relative to themselves, i.e. they sit on sq-NE / sq-NW.
		for _, d := range [2]int{-NE, -NW} {
			s := sq + d
			if OnBoard(s) {
				p := b.Squares[s]
				if p.Type() == Pawn && p.Color() == White {
					return true
				}
			}
		}
	} else {
		for _, d := range [2]int{-SE, -SW} {
			s := sq + d
			if OnBoard(s) {
				p := b.Squares[s]
				if p.Type() == Pawn && p.Color() == Black {
					return true
				}
			}
		}
	}

	// Knights.
	for _, d := range knightOffsets {
		s := sq + d
		if OnBoard(s) {
			p := b.Squares[s]
			if p.Type() == Knight && p.Color() == by {
				return true
			}
		}
	}

	// King (adjacent enemy king attacks).
	for _, d := range kingOffsets {
		s := sq + d
		if OnBoard(s) {
			p := b.Squares[s]
			if p.Type() == King && p.Color() == by {
				return true
			}
		}
	}

	// Bishop/Queen diagonals.
	for _, d := range bishopRays {
		s := sq + d
		for OnBoard(s) {
			p := b.Squares[s]
			if !p.IsEmpty() {
				if p.Color() == by && (p.Type() == Bishop || p.Type() == Queen) {
					return true
				}
				break
			}
			s += d
		}
	}

	// Rook/Queen orthogonals.
	for _, d := range rookRays {
		s := sq + d
		for OnBoard(s) {
			p := b.Squares[s]
			if !p.IsEmpty() {
				if p.Color() == by && (p.Type() == Rook || p.Type() == Queen) {
					return true
				}
				break
			}
			s += d
		}
	}

	return false
}

func (b *Board) InCheck(c Color) bool {
	return b.SquareAttacked(b.KingSquare[c], c.Opp())
}

// GeneratePseudoLegalMoves emits all moves for the side to move that are legal
// modulo king safety (castling-specific safety IS verified here).
func (b *Board) GeneratePseudoLegalMoves(out *[]Move) {
	us := b.SideToMove
	them := us.Opp()

	for sq := 0; sq < 128; sq++ {
		if !OnBoard(sq) {
			continue
		}
		p := b.Squares[sq]
		if p.IsEmpty() || p.Color() != us {
			continue
		}
		switch p.Type() {
		case Pawn:
			b.genPawn(sq, us, out)
		case Knight:
			b.genJumps(sq, us, knightOffsets[:], out)
		case Bishop:
			b.genSlides(sq, us, bishopRays[:], out)
		case Rook:
			b.genSlides(sq, us, rookRays[:], out)
		case Queen:
			b.genSlides(sq, us, queenRays[:], out)
		case King:
			b.genJumps(sq, us, kingOffsets[:], out)
			b.genCastling(sq, us, them, out)
		}
	}
}

func (b *Board) genJumps(from int, us Color, offsets []int, out *[]Move) {
	for _, d := range offsets {
		to := from + d
		if !OnBoard(to) {
			continue
		}
		t := b.Squares[to]
		if t.IsEmpty() || t.Color() != us {
			*out = append(*out, Move{From: from, To: to})
		}
	}
}

func (b *Board) genSlides(from int, us Color, rays []int, out *[]Move) {
	for _, d := range rays {
		to := from + d
		for OnBoard(to) {
			t := b.Squares[to]
			if t.IsEmpty() {
				*out = append(*out, Move{From: from, To: to})
			} else {
				if t.Color() != us {
					*out = append(*out, Move{From: from, To: to})
				}
				break
			}
			to += d
		}
	}
}

func (b *Board) genPawn(from int, us Color, out *[]Move) {
	var fwd, startRank, promoRank int
	if us == White {
		fwd, startRank, promoRank = N, 1, 7
	} else {
		fwd, startRank, promoRank = S, 6, 0
	}

	// Single push.
	one := from + fwd
	if OnBoard(one) && b.Squares[one].IsEmpty() {
		if RankOf(one) == promoRank {
			pushPromos(out, from, one)
		} else {
			*out = append(*out, Move{From: from, To: one})
			// Double push.
			if RankOf(from) == startRank {
				two := one + fwd
				if b.Squares[two].IsEmpty() {
					*out = append(*out, Move{From: from, To: two, Flag: FlagDoublePawn})
				}
			}
		}
	}

	// Captures (and EP).
	for _, side := range [2]int{E, W} {
		to := from + fwd + side
		if !OnBoard(to) {
			continue
		}
		t := b.Squares[to]
		if !t.IsEmpty() && t.Color() != us {
			if RankOf(to) == promoRank {
				pushPromos(out, from, to)
			} else {
				*out = append(*out, Move{From: from, To: to})
			}
		} else if t.IsEmpty() && to == b.EPSquare {
			*out = append(*out, Move{From: from, To: to, Flag: FlagEP})
		}
	}
}

func pushPromos(out *[]Move, from, to int) {
	for _, t := range [4]PieceType{Queen, Rook, Bishop, Knight} {
		*out = append(*out, Move{From: from, To: to, Promo: t})
	}
}

func (b *Board) genCastling(kingSq int, us, them Color, out *[]Move) {
	// Standard chess: castling requires the king on the e-file. Edited
	// positions can leave stale castling-rights bits on a FEN whose king
	// has moved; bail out so we don't fabricate corrupt moves.
	if FileOf(kingSq) != 4 {
		return
	}
	if b.SquareAttacked(kingSq, them) {
		return
	}
	rank := RankOf(kingSq)
	var kRight, qRight uint8
	if us == White {
		kRight, qRight = CastleWK, CastleWQ
	} else {
		kRight, qRight = CastleBK, CastleBQ
	}
	rookOK := func(sq int) bool {
		p := b.Squares[sq]
		return p.Type() == Rook && p.Color() == us
	}
	// Kingside: king e->g, rook h->f. Squares f,g empty, f and g not attacked.
	if b.Castling&kRight != 0 && rookOK(Sq(7, rank)) {
		f := Sq(5, rank)
		g := Sq(6, rank)
		if b.Squares[f].IsEmpty() && b.Squares[g].IsEmpty() &&
			!b.SquareAttacked(f, them) && !b.SquareAttacked(g, them) {
			*out = append(*out, Move{From: kingSq, To: g, Flag: FlagCastleK})
		}
	}
	// Queenside: king e->c, rook a->d. Squares b,c,d empty; c and d not attacked.
	if b.Castling&qRight != 0 && rookOK(Sq(0, rank)) {
		bSq := Sq(1, rank)
		c := Sq(2, rank)
		d := Sq(3, rank)
		if b.Squares[bSq].IsEmpty() && b.Squares[c].IsEmpty() && b.Squares[d].IsEmpty() &&
			!b.SquareAttacked(c, them) && !b.SquareAttacked(d, them) {
			*out = append(*out, Move{From: kingSq, To: c, Flag: FlagCastleQ})
		}
	}
}

// GenerateLegalMoves filters pseudo-legal moves, removing ones that leave own
// king in check. Castling has its in-check / through-check rules already
// enforced by generation.
func (b *Board) GenerateLegalMoves() []Move {
	pseudo := make([]Move, 0, 64)
	b.GeneratePseudoLegalMoves(&pseudo)
	legal := pseudo[:0]
	us := b.SideToMove
	for _, m := range pseudo {
		u := b.MakeMove(m)
		if !b.SquareAttacked(b.KingSquare[us], us.Opp()) {
			legal = append(legal, m)
		}
		b.UnmakeMove(u)
	}
	return legal
}

// GenerateCaptures is like GenerateLegalMoves but only returns captures.
func (b *Board) GenerateCaptures() []Move {
	legal := b.GenerateLegalMoves()
	captures := legal[:0]
	for _, m := range legal {
		if m.IsCapture(b) {
			captures = append(captures, m)
		}
	}
	return captures
}
