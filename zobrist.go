package main

import "math/rand"

// Zobrist hashing: a 64-bit signature that uniquely identifies a position
// for transposition-table and repetition-detection purposes. The signature
// is maintained incrementally by MakeMove / UnmakeMove (XOR in/out the
// pieces, castling rights, en-passant file, and side-to-move that change).

var (
	zPiece  [12][128]uint64 // [pieceZobristIndex][0x88 square]
	zSide   uint64          // XORed in when Black is to move
	zCastle [16]uint64      // indexed by the castling-rights byte (0..15)
	zEP     [8]uint64       // indexed by en-passant target file
)

func init() {
	r := rand.New(rand.NewSource(0xC0FFEECAFE))
	for i := 0; i < 12; i++ {
		for sq := 0; sq < 128; sq++ {
			if OnBoard(sq) {
				zPiece[i][sq] = r.Uint64()
			}
		}
	}
	zSide = r.Uint64()
	for i := 0; i < 16; i++ {
		zCastle[i] = r.Uint64()
	}
	for i := 0; i < 8; i++ {
		zEP[i] = r.Uint64()
	}
}

// pieceZobristIndex maps a non-empty Piece to 0..11 (six types × two colors).
func pieceZobristIndex(p Piece) int {
	return int(p.Type()-1)*2 + int(p.Color())
}

// ComputeHash builds the position signature from scratch. Used by ParseFEN
// and as a sanity check for MakeMove's incremental updates.
func (b *Board) ComputeHash() uint64 {
	var h uint64
	for sq := 0; sq < 128; sq++ {
		if !OnBoard(sq) {
			continue
		}
		p := b.Squares[sq]
		if p.IsEmpty() {
			continue
		}
		h ^= zPiece[pieceZobristIndex(p)][sq]
	}
	if b.SideToMove == Black {
		h ^= zSide
	}
	h ^= zCastle[b.Castling]
	if b.EPSquare != NoSquare {
		h ^= zEP[FileOf(b.EPSquare)]
	}
	return h
}
