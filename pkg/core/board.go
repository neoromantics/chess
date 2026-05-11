package core

import (
	"fmt"
	"strconv"
	"strings"
)

// 0x88 board: valid squares satisfy sq & 0x88 == 0.
// File 0..7 = a..h; rank 0..7 = 1..8; sq = rank*16 + file.

type Color uint8

const (
	White Color = 0
	Black Color = 1
)

func (c Color) Opp() Color { return c ^ 1 }

func (c Color) String() string {
	if c == White {
		return "White"
	}
	return "Black"
}

type PieceType uint8

const (
	Empty PieceType = iota
	Pawn
	Knight
	Bishop
	Rook
	Queen
	King
)

// Piece encodes color in bit 3 and type in bits 0..2. Empty == 0.
type Piece uint8

func MakePiece(t PieceType, c Color) Piece { return Piece(uint8(t) | (uint8(c) << 3)) }
func (p Piece) Type() PieceType            { return PieceType(p & 0x07) }
func (p Piece) Color() Color               { return Color((p >> 3) & 1) }
func (p Piece) IsEmpty() bool              { return p == 0 }

// Castling rights bit-mask.
const (
	CastleWK uint8 = 1 << iota
	CastleWQ
	CastleBK
	CastleBQ
)

const NoSquare = -1

type Board struct {
	Squares        [128]Piece
	SideToMove     Color
	Castling       uint8
	EPSquare       int // 0x88 index of the square *behind* a just-moved double-pushed pawn, or NoSquare
	HalfmoveClock  int
	FullmoveNumber int
	KingSquare     [2]int
	Hash           uint64 // Zobrist signature; maintained incrementally by MakeMove/UnmakeMove
}

// Square helpers ------------------------------------------------------------

func OnBoard(sq int) bool { return sq&0x88 == 0 }
func FileOf(sq int) int   { return sq & 7 }
func RankOf(sq int) int   { return sq >> 4 }
func Sq(file, rank int) int {
	return rank*16 + file
}

func ParseSquare(s string) (int, error) {
	if len(s) != 2 {
		return 0, fmt.Errorf("bad square %q", s)
	}
	f := int(s[0] - 'a')
	r := int(s[1] - '1')
	if f < 0 || f > 7 || r < 0 || r > 7 {
		return 0, fmt.Errorf("bad square %q", s)
	}
	return Sq(f, r), nil
}

func SquareName(sq int) string {
	return string(rune('a'+FileOf(sq))) + string(rune('1'+RankOf(sq)))
}

// FEN ----------------------------------------------------------------------

const StartFEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"

var pieceFromChar = map[byte]Piece{
	'P': MakePiece(Pawn, White), 'N': MakePiece(Knight, White),
	'B': MakePiece(Bishop, White), 'R': MakePiece(Rook, White),
	'Q': MakePiece(Queen, White), 'K': MakePiece(King, White),
	'p': MakePiece(Pawn, Black), 'n': MakePiece(Knight, Black),
	'b': MakePiece(Bishop, Black), 'r': MakePiece(Rook, Black),
	'q': MakePiece(Queen, Black), 'k': MakePiece(King, Black),
}

var pieceToChar = [16]byte{
	0:                        '.',
	uint8(Pawn):              'P',
	uint8(Knight):            'N',
	uint8(Bishop):            'B',
	uint8(Rook):              'R',
	uint8(Queen):             'Q',
	uint8(King):              'K',
	uint8(Pawn) | (1 << 3):   'p',
	uint8(Knight) | (1 << 3): 'n',
	uint8(Bishop) | (1 << 3): 'b',
	uint8(Rook) | (1 << 3):   'r',
	uint8(Queen) | (1 << 3):  'q',
	uint8(King) | (1 << 3):   'k',
}

func ParseFEN(fen string) (*Board, error) {
	parts := strings.Fields(fen)
	if len(parts) < 4 {
		return nil, fmt.Errorf("FEN needs at least 4 fields: %q", fen)
	}
	b := &Board{EPSquare: NoSquare}
	b.KingSquare[White] = NoSquare
	b.KingSquare[Black] = NoSquare

	ranks := strings.Split(parts[0], "/")
	if len(ranks) != 8 {
		return nil, fmt.Errorf("FEN board needs 8 ranks")
	}
	for i, row := range ranks {
		rank := 7 - i
		file := 0
		for j := 0; j < len(row); j++ {
			c := row[j]
			if c >= '1' && c <= '8' {
				file += int(c - '0')
				continue
			}
			p, ok := pieceFromChar[c]
			if !ok {
				return nil, fmt.Errorf("bad FEN piece %q", c)
			}
			if file > 7 {
				return nil, fmt.Errorf("FEN rank overflow")
			}
			sq := Sq(file, rank)
			b.Squares[sq] = p
			if p.Type() == King {
				b.KingSquare[p.Color()] = sq
			}
			file++
		}
		if file != 8 {
			return nil, fmt.Errorf("FEN rank %d: expected 8 files, got %d", rank+1, file)
		}
	}

	switch parts[1] {
	case "w":
		b.SideToMove = White
	case "b":
		b.SideToMove = Black
	default:
		return nil, fmt.Errorf("bad side %q", parts[1])
	}

	if parts[2] != "-" {
		for j := 0; j < len(parts[2]); j++ {
			switch parts[2][j] {
			case 'K':
				b.Castling |= CastleWK
			case 'Q':
				b.Castling |= CastleWQ
			case 'k':
				b.Castling |= CastleBK
			case 'q':
				b.Castling |= CastleBQ
			default:
				return nil, fmt.Errorf("bad castling %q", parts[2])
			}
		}
	}

	if parts[3] == "-" {
		b.EPSquare = NoSquare
	} else {
		sq, err := ParseSquare(parts[3])
		if err != nil {
			return nil, err
		}
		b.EPSquare = sq
	}

	b.HalfmoveClock = 0
	b.FullmoveNumber = 1
	if len(parts) >= 5 {
		if v, err := strconv.Atoi(parts[4]); err == nil {
			b.HalfmoveClock = v
		}
	}
	if len(parts) >= 6 {
		if v, err := strconv.Atoi(parts[5]); err == nil {
			b.FullmoveNumber = v
		}
	}

	if b.KingSquare[White] == NoSquare || b.KingSquare[Black] == NoSquare {
		return nil, fmt.Errorf("FEN missing a king")
	}
	b.Hash = b.ComputeHash()
	return b, nil
}

func StartPosition() *Board {
	b, err := ParseFEN(StartFEN)
	if err != nil {
		panic(err)
	}
	return b
}

func (b *Board) FEN() string {
	var sb strings.Builder
	for rank := 7; rank >= 0; rank-- {
		empty := 0
		for file := 0; file < 8; file++ {
			p := b.Squares[Sq(file, rank)]
			if p.IsEmpty() {
				empty++
				continue
			}
			if empty > 0 {
				sb.WriteByte(byte('0' + empty))
				empty = 0
			}
			sb.WriteByte(pieceToChar[p])
		}
		if empty > 0 {
			sb.WriteByte(byte('0' + empty))
		}
		if rank > 0 {
			sb.WriteByte('/')
		}
	}
	sb.WriteByte(' ')
	if b.SideToMove == White {
		sb.WriteByte('w')
	} else {
		sb.WriteByte('b')
	}
	sb.WriteByte(' ')
	if b.Castling == 0 {
		sb.WriteByte('-')
	} else {
		if b.Castling&CastleWK != 0 {
			sb.WriteByte('K')
		}
		if b.Castling&CastleWQ != 0 {
			sb.WriteByte('Q')
		}
		if b.Castling&CastleBK != 0 {
			sb.WriteByte('k')
		}
		if b.Castling&CastleBQ != 0 {
			sb.WriteByte('q')
		}
	}
	sb.WriteByte(' ')
	if b.EPSquare == NoSquare {
		sb.WriteByte('-')
	} else {
		sb.WriteString(SquareName(b.EPSquare))
	}
	fmt.Fprintf(&sb, " %d %d", b.HalfmoveClock, b.FullmoveNumber)
	return sb.String()
}

// Display ------------------------------------------------------------------

var unicodePiece = map[Piece]string{
	MakePiece(King, White):   "♔",
	MakePiece(Queen, White):  "♕",
	MakePiece(Rook, White):   "♖",
	MakePiece(Bishop, White): "♗",
	MakePiece(Knight, White): "♘",
	MakePiece(Pawn, White):   "♙",
	MakePiece(King, Black):   "♚",
	MakePiece(Queen, Black):  "♛",
	MakePiece(Rook, Black):   "♜",
	MakePiece(Bishop, Black): "♝",
	MakePiece(Knight, Black): "♞",
	MakePiece(Pawn, Black):   "♟",
}

func (b *Board) Display() string {
	var sb strings.Builder
	for rank := 7; rank >= 0; rank-- {
		fmt.Fprintf(&sb, "%d ", rank+1)
		for file := 0; file < 8; file++ {
			p := b.Squares[Sq(file, rank)]
			if p.IsEmpty() {
				sb.WriteString(". ")
			} else {
				sb.WriteString(unicodePiece[p])
				sb.WriteByte(' ')
			}
		}
		sb.WriteByte('\n')
	}
	sb.WriteString("  a b c d e f g h\n")
	return sb.String()
}

// InsufficientMaterial recognises the standard FIDE draws by material:
// K vs K, K + (B or N) vs K, and K + B vs K + B with same-square-color
// bishops.
func (b *Board) InsufficientMaterial() bool {
	var counts [2][7]int
	bishopColor := [2]int{-1, -1}
	for sq := 0; sq < 128; sq++ {
		if !OnBoard(sq) {
			continue
		}
		p := b.Squares[sq]
		if p.IsEmpty() {
			continue
		}
		counts[p.Color()][p.Type()]++
		if p.Type() == Bishop {
			bishopColor[p.Color()] = (FileOf(sq) + RankOf(sq)) % 2
		}
	}
	for c := 0; c < 2; c++ {
		if counts[c][Pawn] > 0 || counts[c][Rook] > 0 || counts[c][Queen] > 0 {
			return false
		}
	}
	nW := counts[White][Knight] + counts[White][Bishop]
	nB := counts[Black][Knight] + counts[Black][Bishop]
	if nW == 0 && nB == 0 {
		return true
	}
	if (nW == 1 && nB == 0) || (nB == 1 && nW == 0) {
		return true
	}
	if counts[White][Bishop] == 1 && counts[Black][Bishop] == 1 &&
		counts[White][Knight] == 0 && counts[Black][Knight] == 0 &&
		bishopColor[White] == bishopColor[Black] {
		return true
	}
	return false
}
