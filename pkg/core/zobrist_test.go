package core

import "testing"

// hashRoundTrip walks the move tree to depth d, asserting that the
// incrementally-maintained Hash field matches ComputeHash() at every
// node and that UnmakeMove restores the previous hash exactly.
func hashRoundTrip(t *testing.T, b *Board, depth int) {
	t.Helper()
	if depth == 0 {
		if got, want := b.Hash, b.ComputeHash(); got != want {
			t.Fatalf("hash mismatch at leaf: got %x, want %x\nFEN: %s", got, want, b.FEN())
		}
		return
	}
	for _, m := range b.GenerateLegalMoves() {
		before := b.Hash
		u := b.MakeMove(m)
		if got, want := b.Hash, b.ComputeHash(); got != want {
			t.Fatalf("hash mismatch after %s: got %x, want %x\nFEN: %s",
				m.String(), got, want, b.FEN())
		}
		hashRoundTrip(t, b, depth-1)
		b.UnmakeMove(u)
		if b.Hash != before {
			t.Fatalf("Unmake of %s did not restore hash: got %x, want %x",
				m.String(), b.Hash, before)
		}
	}
}

func TestZobristStartpos(t *testing.T) {
	b := StartPosition()
	hashRoundTrip(t, b, 3) // ~9000 nodes; thorough enough
}

// Kiwipete exercises every special-case move (castling, EP, promotion).
func TestZobristKiwipete(t *testing.T) {
	b, err := ParseFEN("r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	hashRoundTrip(t, b, 2)
}

// Position with promotion choices.
func TestZobristPromotions(t *testing.T) {
	b, err := ParseFEN("r3k2r/Pppp1ppp/1b3nbN/nP6/BBP1P3/q4N2/Pp1P2PP/R2Q1RK1 w kq - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	hashRoundTrip(t, b, 2)
}
