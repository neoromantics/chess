package pgn

import (
	"strings"
	"testing"
)

func TestEncodeStandardStart(t *testing.T) {
	got := Encode(Headers{White: "alice", Black: "engine"}, "", []string{"e4", "e5", "Nf3", "Nc6"}, "*")
	if !strings.Contains(got, `[White "alice"]`) {
		t.Errorf("missing White header in %q", got)
	}
	if !strings.Contains(got, "1. e4 e5 2. Nf3 Nc6 *") {
		t.Errorf("movetext wrong: %q", got)
	}
	// No FEN header for standard start.
	if strings.Contains(got, "[FEN") {
		t.Errorf("standard-start game should not have [FEN] header: %q", got)
	}
}

func TestEncodeFromCustomFEN(t *testing.T) {
	fen := "4k3/8/8/8/8/8/4Q3/4K3 b - - 5 1"
	got := Encode(Headers{}, fen, nil, "1-0")
	if !strings.Contains(got, "[SetUp \"1\"]") || !strings.Contains(got, "[FEN ") {
		t.Errorf("expected SetUp + FEN tags: %q", got)
	}
}

func TestDecodeRoundTrip(t *testing.T) {
	src := `[Event "Casual Game"]
[Site "?"]
[Date "????.??.??"]
[Round "-"]
[White "alice"]
[Black "engine"]
[Result "*"]

1. e4 e5 2. Nf3 Nc6 3. Bb5 a6 *
`
	d, err := Decode(src)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []string{"e2e4", "e7e5", "g1f3", "b8c6", "f1b5", "a7a6"}
	if len(d.UCIMoves) != len(want) {
		t.Fatalf("got %d moves, want %d: %v", len(d.UCIMoves), len(want), d.UCIMoves)
	}
	for i, m := range want {
		if d.UCIMoves[i] != m {
			t.Errorf("move %d: got %q want %q", i, d.UCIMoves[i], m)
		}
	}
	if d.Result != "*" {
		t.Errorf("result: got %q want *", d.Result)
	}
}

func TestDecodeCommentsAndVariations(t *testing.T) {
	src := `[Event "test"]
[Result "1-0"]

1. e4 {good move} (1. d4 d5) 1...e5 2. Nf3 Nc6 1-0
`
	d, err := Decode(src)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []string{"e2e4", "e7e5", "g1f3", "b8c6"}
	if len(d.UCIMoves) != len(want) {
		t.Fatalf("got moves %v, want %v", d.UCIMoves, want)
	}
	if d.Result != "1-0" {
		t.Errorf("result: got %q want 1-0", d.Result)
	}
}

func TestDecodeFromCustomFEN(t *testing.T) {
	src := `[Event "endgame"]
[FEN "4k3/8/8/8/8/8/4P3/4K3 w - - 0 1"]
[SetUp "1"]
[Result "*"]

1. e3 Kd7 *
`
	d, err := Decode(src)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.StartFEN == "" {
		t.Errorf("expected StartFEN to be populated")
	}
	if len(d.UCIMoves) != 2 || d.UCIMoves[0] != "e2e3" || d.UCIMoves[1] != "e8d7" {
		t.Errorf("got moves %v, want [e2e3 e8d7]", d.UCIMoves)
	}
}

func TestDecodeIllegalMoveFails(t *testing.T) {
	// Kf2 is illegal on move 1 — the king is blocked by its own pawns.
	src := `[Result "*"]

1. Kf2 *
`
	if _, err := Decode(src); err == nil {
		t.Fatalf("expected error for illegal opening move Kf2")
	}
}
