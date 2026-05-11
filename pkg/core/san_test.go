package core

import "testing"

// playSAN plays moves on g and returns the SAN sequence in order.
func playSAN(t *testing.T, fen string, moves []string) []string {
	t.Helper()
	g := NewGame()
	if err := g.Load(fen, nil, false, false); err != nil {
		t.Fatal(err)
	}
	for _, ms := range moves {
		mustMove(t, g, ms)
	}
	return g.HistorySAN
}

func TestSANBasicMoves(t *testing.T) {
	got := playSAN(t, StartFEN, []string{"e2e4", "e7e5", "g1f3", "b8c6", "f1c4"})
	want := []string{"e4", "e5", "Nf3", "Nc6", "Bc4"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("ply %d: got %q, want %q", i, got[i], w)
		}
	}
}

func TestSANCastling(t *testing.T) {
	// Set up a position with both sides ready to castle kingside.
	got := playSAN(t,
		"r3k2r/pppppppp/8/8/8/8/PPPPPPPP/R3K2R w KQkq - 0 1",
		[]string{"e1g1", "e8c8"},
	)
	if got[0] != "O-O" {
		t.Errorf("kingside got %q, want O-O", got[0])
	}
	if got[1] != "O-O-O" {
		t.Errorf("queenside got %q, want O-O-O", got[1])
	}
}

func TestSANPawnCapture(t *testing.T) {
	got := playSAN(t, StartFEN, []string{"e2e4", "d7d5", "e4d5"})
	if got[2] != "exd5" {
		t.Errorf("got %q, want exd5", got[2])
	}
}

func TestSANEnPassant(t *testing.T) {
	// Set up: white pawn on e5, black plays d7-d5; en passant exd6.
	got := playSAN(t,
		"4k3/3p4/8/4P3/8/8/8/4K3 b - - 0 1",
		[]string{"d7d5", "e5d6"},
	)
	if got[1] != "exd6" {
		t.Errorf("got %q, want exd6", got[1])
	}
}

func TestSANPromotionAndCheck(t *testing.T) {
	// Black king on a8 — promoting on e8 gives check along rank 8.
	got := playSAN(t, "k7/4P3/8/8/8/8/8/4K3 w - - 0 1", []string{"e7e8q"})
	if got[0] != "e8=Q+" {
		t.Errorf("got %q, want e8=Q+", got[0])
	}
}

func TestSANCheckmateSuffix(t *testing.T) {
	// Fool's mate.
	got := playSAN(t, StartFEN, []string{"f2f3", "e7e5", "g2g4", "d8h4"})
	if got[3] != "Qh4#" {
		t.Errorf("got %q, want Qh4#", got[3])
	}
}

func TestSANKnightDisambiguationFile(t *testing.T) {
	// Both knights on c3 and g1 can reach e2 — file disambiguates.
	got := playSAN(t,
		"4k3/8/8/8/8/2N5/8/4K1N1 w - - 0 1",
		[]string{"c3e2"},
	)
	// Wait — only one knight (c3) reaches e2. g1 reaches e2 too (knight g1->e2 is legal: g-e is 2 files, 1 rank).
	// So both knights can land on e2. File disambiguation: "Nce2".
	if got[0] != "Nce2" {
		t.Errorf("got %q, want Nce2", got[0])
	}
}

func TestSANKnightDisambiguationRank(t *testing.T) {
	// Two knights on the same file (c3 and c5) both targeting e4.
	// c3->e4: 2 files, 1 rank — knight move.
	// c5->e4: 2 files, 1 rank — knight move.
	// Files match, so rank disambiguation: "N3e4" / "N5e4".
	got := playSAN(t,
		"4k3/8/8/2N5/8/2N5/8/4K3 w - - 0 1",
		[]string{"c3e4"},
	)
	if got[0] != "N3e4" {
		t.Errorf("got %q, want N3e4", got[0])
	}
}

func TestSANNoUnneededDisambiguation(t *testing.T) {
	// Single knight on g1 to f3 from startpos — just "Nf3".
	got := playSAN(t, StartFEN, []string{"g1f3"})
	if got[0] != "Nf3" {
		t.Errorf("got %q, want Nf3", got[0])
	}
}
