package core

import "fmt"

type MoveFlag uint8

const (
	FlagNormal MoveFlag = iota
	FlagDoublePawn
	FlagEP
	FlagCastleK // king-side
	FlagCastleQ // queen-side
)

type Move struct {
	From, To int
	Promo    PieceType // Empty if not a promotion
	Flag     MoveFlag
}

// Equal compares moves by source, destination, and promotion piece. Move
// flags are derived from those plus board state, so they are not part of
// equality.
func (m Move) Equal(o Move) bool {
	return m.From == o.From && m.To == o.To && m.Promo == o.Promo
}

// Long-algebraic notation, e.g. "e2e4" or "e7e8q".
func (m Move) String() string {
	s := SquareName(m.From) + SquareName(m.To)
	if m.Promo != Empty {
		s += string(PromoChar(m.Promo))
	}
	return s
}

func PromoChar(t PieceType) byte {
	switch t {
	case Knight:
		return 'n'
	case Bishop:
		return 'b'
	case Rook:
		return 'r'
	case Queen:
		return 'q'
	}
	return '?'
}

func promoFromChar(c byte) PieceType {
	switch c {
	case 'n', 'N':
		return Knight
	case 'b', 'B':
		return Bishop
	case 'r', 'R':
		return Rook
	case 'q', 'Q':
		return Queen
	}
	return Empty
}

// ParseMove parses long algebraic and resolves Flag/Promo against the board.
// Used for UCI "position ... moves ...".
func (b *Board) ParseUCIMove(s string) (Move, error) {
	if len(s) < 4 || len(s) > 5 {
		return Move{}, fmt.Errorf("bad move %q", s)
	}
	from, err := ParseSquare(s[0:2])
	if err != nil {
		return Move{}, err
	}
	to, err := ParseSquare(s[2:4])
	if err != nil {
		return Move{}, err
	}
	m := Move{From: from, To: to}
	if len(s) == 5 {
		m.Promo = promoFromChar(s[4])
		if m.Promo == Empty {
			return Move{}, fmt.Errorf("bad promo %q", s)
		}
	}
	p := b.Squares[from]
	if p.IsEmpty() {
		return Move{}, fmt.Errorf("no piece on %s", SquareName(from))
	}
	if p.Type() == King && abs(FileOf(to)-FileOf(from)) == 2 {
		if FileOf(to) == 6 {
			m.Flag = FlagCastleK
		} else {
			m.Flag = FlagCastleQ
		}
	} else if p.Type() == Pawn {
		if abs(RankOf(to)-RankOf(from)) == 2 {
			m.Flag = FlagDoublePawn
		} else if to == b.EPSquare && b.Squares[to].IsEmpty() && FileOf(to) != FileOf(from) {
			m.Flag = FlagEP
		}
	}
	return m, nil
}

// Undo holds the information needed to revert a MakeMove.
type Undo struct {
	Move           Move
	Captured       Piece // piece removed from the board (or Empty); for EP, the pawn behind To
	PrevCastling   uint8
	PrevEPSquare   int
	PrevHalfmove   int
	PrevKingSquare int    // king square of side-to-move *before* the move (for fast restore)
	PrevHash       uint64 // Zobrist signature before the move
}

// castleClear[sq] is the set of castling-rights bits to drop whenever a piece
// moves to or from sq (e.g. king moves, rooks move, rooks captured on corners).
var castleClear [128]uint8

func init() {
	castleClear[Sq(0, 0)] = CastleWQ            // a1
	castleClear[Sq(7, 0)] = CastleWK            // h1
	castleClear[Sq(4, 0)] = CastleWK | CastleWQ // e1
	castleClear[Sq(0, 7)] = CastleBQ            // a8
	castleClear[Sq(7, 7)] = CastleBK            // h8
	castleClear[Sq(4, 7)] = CastleBK | CastleBQ // e8
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func (b *Board) MakeMove(m Move) Undo {
	us := b.SideToMove
	them := us.Opp()
	piece := b.Squares[m.From]

	u := Undo{
		Move:           m,
		PrevCastling:   b.Castling,
		PrevEPSquare:   b.EPSquare,
		PrevHalfmove:   b.HalfmoveClock,
		PrevKingSquare: b.KingSquare[us],
		PrevHash:       b.Hash,
	}

	// Hash bookkeeping: XOR out the parts of the position that are about
	// to change (castling rights, EP file, side). New values are XORed
	// back in at the end after they've been computed.
	b.Hash ^= zCastle[b.Castling]
	if b.EPSquare != NoSquare {
		b.Hash ^= zEP[FileOf(b.EPSquare)]
	}
	b.Hash ^= zSide

	// Capture detection.
	if m.Flag == FlagEP {
		// Captured pawn sits behind the EP target square (from the mover's POV).
		capSq := m.To
		if us == White {
			capSq -= 16
		} else {
			capSq += 16
		}
		u.Captured = b.Squares[capSq]
		b.Hash ^= zPiece[pieceZobristIndex(u.Captured)][capSq]
		b.Squares[capSq] = 0
	} else if !b.Squares[m.To].IsEmpty() {
		u.Captured = b.Squares[m.To]
		b.Hash ^= zPiece[pieceZobristIndex(u.Captured)][m.To]
	}

	// Move the piece.
	b.Hash ^= zPiece[pieceZobristIndex(piece)][m.From]
	b.Squares[m.From] = 0
	if m.Promo == Empty {
		b.Squares[m.To] = piece
		b.Hash ^= zPiece[pieceZobristIndex(piece)][m.To]
	} else {
		// Promotion: a different piece-type lands on the destination.
		promoted := MakePiece(m.Promo, us)
		b.Squares[m.To] = promoted
		b.Hash ^= zPiece[pieceZobristIndex(promoted)][m.To]
	}

	// Castling: also move the rook.
	switch m.Flag {
	case FlagCastleK:
		rookFrom := Sq(7, RankOf(m.From))
		rookTo := Sq(5, RankOf(m.From))
		rook := b.Squares[rookFrom]
		b.Squares[rookTo] = rook
		b.Squares[rookFrom] = 0
		b.Hash ^= zPiece[pieceZobristIndex(rook)][rookFrom]
		b.Hash ^= zPiece[pieceZobristIndex(rook)][rookTo]
	case FlagCastleQ:
		rookFrom := Sq(0, RankOf(m.From))
		rookTo := Sq(3, RankOf(m.From))
		rook := b.Squares[rookFrom]
		b.Squares[rookTo] = rook
		b.Squares[rookFrom] = 0
		b.Hash ^= zPiece[pieceZobristIndex(rook)][rookFrom]
		b.Hash ^= zPiece[pieceZobristIndex(rook)][rookTo]
	}

	// Update king square if king moved.
	if piece.Type() == King {
		b.KingSquare[us] = m.To
	}

	// Update castling rights (movement of king/rook, capture of rook on corner).
	b.Castling &^= castleClear[m.From] | castleClear[m.To]
	b.Hash ^= zCastle[b.Castling]

	// Update EP square.
	if m.Flag == FlagDoublePawn {
		if us == White {
			b.EPSquare = m.From + 16
		} else {
			b.EPSquare = m.From - 16
		}
		b.Hash ^= zEP[FileOf(b.EPSquare)]
	} else {
		b.EPSquare = NoSquare
	}

	// Halfmove clock.
	if piece.Type() == Pawn || u.Captured != 0 {
		b.HalfmoveClock = 0
	} else {
		b.HalfmoveClock++
	}

	// Fullmove number after Black moves.
	if us == Black {
		b.FullmoveNumber++
	}

	b.SideToMove = them
	return u
}

// NullUndo lets UnmakeNullMove restore state changed by MakeNullMove.
type NullUndo struct {
	PrevEPSquare int
	PrevHash     uint64
}

// MakeNullMove flips the side to move without moving any piece. Used by
// null-move pruning to test whether the position is so good that even
// "passing" the move leaves the side to move winning. EP rights are
// cleared for the null position because no pawn just moved.
func (b *Board) MakeNullMove() NullUndo {
	u := NullUndo{PrevEPSquare: b.EPSquare, PrevHash: b.Hash}
	if b.EPSquare != NoSquare {
		b.Hash ^= zEP[FileOf(b.EPSquare)]
		b.EPSquare = NoSquare
	}
	b.Hash ^= zSide
	b.SideToMove = b.SideToMove.Opp()
	return u
}

// UnmakeNullMove reverses MakeNullMove.
func (b *Board) UnmakeNullMove(u NullUndo) {
	b.SideToMove = b.SideToMove.Opp()
	b.EPSquare = u.PrevEPSquare
	b.Hash = u.PrevHash
}

func (b *Board) UnmakeMove(u Undo) {
	m := u.Move
	them := b.SideToMove
	us := them.Opp() // side that originally made the move

	// Flip side first so that "us" matches the side that made the move.
	b.SideToMove = us
	if us == Black {
		b.FullmoveNumber--
	}
	b.Castling = u.PrevCastling
	b.EPSquare = u.PrevEPSquare
	b.HalfmoveClock = u.PrevHalfmove
	b.Hash = u.PrevHash

	// Move piece back. For promotion, restore the pawn at From.
	piece := b.Squares[m.To]
	if m.Promo != Empty {
		piece = MakePiece(Pawn, us)
	}
	b.Squares[m.From] = piece
	b.Squares[m.To] = 0

	// Restore captured piece (EP captured square is the pawn behind To).
	if m.Flag == FlagEP {
		capSq := m.To
		if us == White {
			capSq -= 16
		} else {
			capSq += 16
		}
		b.Squares[capSq] = u.Captured
	} else if u.Captured != 0 {
		b.Squares[m.To] = u.Captured
	}

	// Castling: move rook back.
	switch m.Flag {
	case FlagCastleK:
		rookFrom := Sq(7, RankOf(m.From))
		rookTo := Sq(5, RankOf(m.From))
		b.Squares[rookFrom] = b.Squares[rookTo]
		b.Squares[rookTo] = 0
	case FlagCastleQ:
		rookFrom := Sq(0, RankOf(m.From))
		rookTo := Sq(3, RankOf(m.From))
		b.Squares[rookFrom] = b.Squares[rookTo]
		b.Squares[rookTo] = 0
	}

	// Restore king square.
	if piece.Type() == King {
		b.KingSquare[us] = u.PrevKingSquare
	}
}

func (m Move) IsCapture(b *Board) bool {
	return !b.Squares[m.To].IsEmpty() || m.Flag == FlagEP
}
