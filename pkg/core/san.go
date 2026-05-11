package core

import "strings"

// MoveToSAN returns the standard algebraic notation for m played on b.
// b must be in the position *before* m has been made; the board is left
// unchanged on return.
func MoveToSAN(b *Board, m Move) string {
	// Castling.
	if m.Flag == FlagCastleK {
		return sanWithCheckSuffix(b, m, "O-O")
	}
	if m.Flag == FlagCastleQ {
		return sanWithCheckSuffix(b, m, "O-O-O")
	}

	piece := b.Squares[m.From]
	t := piece.Type()
	isCapture := !b.Squares[m.To].IsEmpty() || m.Flag == FlagEP

	var sb strings.Builder
	if t == Pawn {
		// Pawn captures include the source file: "exd5", not "xd5".
		if isCapture {
			sb.WriteByte(byte('a' + FileOf(m.From)))
		}
	} else {
		sb.WriteByte(pieceSANLetter(t))
		sb.WriteString(sanDisambiguation(b, m, t))
	}
	if isCapture {
		sb.WriteByte('x')
	}
	sb.WriteString(SquareName(m.To))
	if m.Promo != Empty {
		sb.WriteByte('=')
		sb.WriteByte(pieceSANLetter(m.Promo))
	}
	return sanWithCheckSuffix(b, m, sb.String())
}

func pieceSANLetter(t PieceType) byte {
	switch t {
	case King:
		return 'K'
	case Queen:
		return 'Q'
	case Rook:
		return 'R'
	case Bishop:
		return 'B'
	case Knight:
		return 'N'
	}
	return '?'
}

// sanDisambiguation returns the prefix needed when more than one same-typed
// piece could legally move to m.To. Standard rule: prefer file, then rank,
// then both.
func sanDisambiguation(b *Board, m Move, t PieceType) string {
	legal := b.GenerateLegalMoves()
	// Find every legal move of this piece type that targets the same
	// destination — including m itself, which we'll exclude when checking
	// for ambiguity.
	var sources []int
	for _, lm := range legal {
		if lm.To != m.To {
			continue
		}
		if b.Squares[lm.From].Type() != t {
			continue
		}
		sources = append(sources, lm.From)
	}
	if len(sources) <= 1 {
		return ""
	}
	fromFile := FileOf(m.From)
	fromRank := RankOf(m.From)
	fileUnique, rankUnique := true, true
	for _, s := range sources {
		if s == m.From {
			continue
		}
		if FileOf(s) == fromFile {
			fileUnique = false
		}
		if RankOf(s) == fromRank {
			rankUnique = false
		}
	}
	switch {
	case fileUnique:
		return string(byte('a' + fromFile))
	case rankUnique:
		return string(byte('1' + fromRank))
	default:
		return string([]byte{byte('a' + fromFile), byte('1' + fromRank)})
	}
}

// sanWithCheckSuffix appends "+" (check) or "#" (mate) by playing the move
// transiently. The board is restored before return.
func sanWithCheckSuffix(b *Board, m Move, base string) string {
	u := b.MakeMove(m)
	them := b.SideToMove
	inCheck := b.SquareAttacked(b.KingSquare[them], them.Opp())
	suffix := ""
	if inCheck {
		if len(b.GenerateLegalMoves()) == 0 {
			suffix = "#"
		} else {
			suffix = "+"
		}
	}
	b.UnmakeMove(u)
	return base + suffix
}
