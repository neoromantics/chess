package rating

import (
	"math"
	"testing"
)

// TestUpdateAgainstPaperExample reproduces the worked example from
// Glickman's "Example calculations" appendix (2013 revision of the
// Glicko-2 paper, http://www.glicko.net/glicko/glicko2.pdf).
//
//	Player: r=1500, RD=200, σ=0.06
//	Opponents (single rating period):
//	    r=1400, RD=30,  score=1.0 (win)
//	    r=1550, RD=100, score=0.0 (loss)
//	    r=1700, RD=300, score=0.0 (loss)
//
// Expected (per paper, page 4):
//
//	r' ≈ 1464.06
//	RD' ≈ 151.52
//	σ' ≈ 0.05999
//
// This was the explicit correctness gate the platform owners set
// before relying on rating numbers in production.
func TestUpdateAgainstPaperExample(t *testing.T) {
	p := Player{Rating: 1500, RD: 200, Volatility: 0.06}
	opps := []Opponent{
		{P: Player{Rating: 1400, RD: 30, Volatility: 0.06}, Score: 1.0},
		{P: Player{Rating: 1550, RD: 100, Volatility: 0.06}, Score: 0.0},
		{P: Player{Rating: 1700, RD: 300, Volatility: 0.06}, Score: 0.0},
	}
	got := Update(p, opps)

	type expect struct{ rating, rd, sigma, tol float64 }
	want := expect{rating: 1464.06, rd: 151.52, sigma: 0.05999, tol: 0.05}

	approxEq := func(label string, g, e, tol float64) {
		t.Helper()
		if math.Abs(g-e) > tol {
			t.Errorf("%s: got %.4f, want %.4f (±%.4f)", label, g, e, tol)
		}
	}
	approxEq("rating", got.Rating, want.rating, want.tol)
	approxEq("RD", got.RD, want.rd, want.tol)
	// Volatility moves slowly; tighten the tolerance.
	approxEq("sigma", got.Volatility, want.sigma, 0.0001)
}

func TestUpdateNoGamesOnlyInflatesRD(t *testing.T) {
	p := Player{Rating: 1500, RD: 200, Volatility: 0.06}
	got := Update(p, nil)
	if got.Rating != p.Rating {
		t.Errorf("rating should not move without games: got %.4f want %.4f", got.Rating, p.Rating)
	}
	if got.RD <= p.RD {
		t.Errorf("RD should grow when no games are played: got %.4f want > %.4f", got.RD, p.RD)
	}
	if got.Volatility != p.Volatility {
		t.Errorf("volatility should not move without games: got %.6f want %.6f", got.Volatility, p.Volatility)
	}
}
