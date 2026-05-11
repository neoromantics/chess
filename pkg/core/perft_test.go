package core

import "testing"

func perft(b *Board, depth int) int64 {
	if depth == 0 {
		return 1
	}
	moves := b.GenerateLegalMoves()
	if depth == 1 {
		return int64(len(moves))
	}
	var n int64
	for _, m := range moves {
		u := b.MakeMove(m)
		n += perft(b, depth-1)
		b.UnmakeMove(u)
	}
	return n
}

func TestPerftStartpos(t *testing.T) {
	cases := []struct {
		depth int
		want  int64
	}{
		{1, 20},
		{2, 400},
		{3, 8902},
		{4, 197281},
	}
	b := StartPosition()
	for _, c := range cases {
		got := perft(b, c.depth)
		if got != c.want {
			t.Errorf("perft startpos depth %d = %d, want %d", c.depth, got, c.want)
		}
	}
}

// "Kiwipete" — exercises castling, EP, promotions, complex tactics.
func TestPerftKiwipete(t *testing.T) {
	b, err := ParseFEN("r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		depth int
		want  int64
	}{
		{1, 48},
		{2, 2039},
		{3, 97862},
	}
	for _, c := range cases {
		got := perft(b, c.depth)
		if got != c.want {
			t.Errorf("perft kiwipete depth %d = %d, want %d", c.depth, got, c.want)
		}
	}
}

// Position 3 from chessprogramming wiki — pawn promotion, EP, check evasions.
func TestPerftPos3(t *testing.T) {
	b, err := ParseFEN("8/2p5/3p4/KP5r/1R3p1k/8/4P1P1/8 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		depth int
		want  int64
	}{
		{1, 14},
		{2, 191},
		{3, 2812},
		{4, 43238},
	}
	for _, c := range cases {
		got := perft(b, c.depth)
		if got != c.want {
			t.Errorf("perft pos3 depth %d = %d, want %d", c.depth, got, c.want)
		}
	}
}

// Position 4 — promotions galore.
func TestPerftPos4(t *testing.T) {
	b, err := ParseFEN("r3k2r/Pppp1ppp/1b3nbN/nP6/BBP1P3/q4N2/Pp1P2PP/R2Q1RK1 w kq - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		depth int
		want  int64
	}{
		{1, 6},
		{2, 264},
		{3, 9467},
	}
	for _, c := range cases {
		got := perft(b, c.depth)
		if got != c.want {
			t.Errorf("perft pos4 depth %d = %d, want %d", c.depth, got, c.want)
		}
	}
}
