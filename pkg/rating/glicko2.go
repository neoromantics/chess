package rating

import (
	"math"
)

// Standard Glicko-2 constants as recommended by Mark Glickman.
const (
	// DefaultRating is the starting rating for new players.
	DefaultRating = 1500.0
	// DefaultRD is the starting rating deviation.
	DefaultRD = 350.0
	// DefaultVolatility is the starting volatility.
	DefaultVolatility = 0.06
	// Tau is the system constant that constrains the change in volatility over
	// time (typically between 0.3 and 1.2).
	Tau = 0.5
)

// Player represents a Glicko-2 rating profile.
type Player struct {
	Rating     float64
	RD         float64
	Volatility float64
}

// NewPlayer returns a player with default Glicko-2 priors.
func NewPlayer() Player {
	return Player{
		Rating:     DefaultRating,
		RD:         DefaultRD,
		Volatility: DefaultVolatility,
	}
}

// Result represents a match outcome: 1.0 for win, 0.5 for draw, 0.0 for loss.
type Result float64

// Opponent is a record of a single game against another player.
type Opponent struct {
	P     Player
	Score Result
}

// Glicko-2 internal conversion constants.
const (
	g2Scaling = 173.7178
)

// Update computes the new rating profile for a player after a series of
// games in a single rating period.
func Update(p Player, opponents []Opponent) Player {
	// Step 2: Convert to Glicko-2 scale
	mu := (p.Rating - DefaultRating) / g2Scaling
	phi := p.RD / g2Scaling
	sigma := p.Volatility

	if len(opponents) == 0 {
		// Step 6: If no games, only RD increases due to uncertainty
		phiPrime := math.Sqrt(phi*phi + sigma*sigma)
		return Player{
			Rating:     p.Rating,
			RD:         phiPrime * g2Scaling,
			Volatility: sigma,
		}
	}

	// Step 3: Compute estimated variance v
	var vInv float64
	for _, opp := range opponents {
		muOpp := (opp.P.Rating - DefaultRating) / g2Scaling
		phiOpp := opp.P.RD / g2Scaling
		gPhi := 1.0 / math.Sqrt(1.0+3.0*phiOpp*phiOpp/(math.Pi*math.Pi))
		e := 1.0 / (1.0 + math.Exp(-gPhi*(mu-muOpp)))
		vInv += gPhi * gPhi * e * (1.0 - e)
	}
	v := 1.0 / vInv

	// Step 4: Compute improvement delta
	var deltaPart float64
	for _, opp := range opponents {
		muOpp := (opp.P.Rating - DefaultRating) / g2Scaling
		phiOpp := opp.P.RD / g2Scaling
		gPhi := 1.0 / math.Sqrt(1.0+3.0*phiOpp*phiOpp/(math.Pi*math.Pi))
		e := 1.0 / (1.0 + math.Exp(-gPhi*(mu-muOpp)))
		deltaPart += gPhi * (float64(opp.Score) - e)
	}
	delta := v * deltaPart

	// Step 5: Compute new volatility sigmaPrime
	sigmaPrime := updateVolatility(sigma, phi, v, delta)

	// Step 6: Update RD to phiStar
	phiStar := math.Sqrt(phi*phi + sigmaPrime*sigmaPrime)

	// Step 7: Update rating and RD to new values
	phiPrime := 1.0 / math.Sqrt(1.0/(phiStar*phiStar)+1.0/v)
	muPrime := mu + phiPrime*phiPrime*deltaPart

	// Step 8: Convert back to original scale
	return Player{
		Rating:     DefaultRating + g2Scaling*muPrime,
		RD:         g2Scaling * phiPrime,
		Volatility: sigmaPrime,
	}
}

// updateVolatility uses the Illinois algorithm (Step 5 of Glicko-2) to
// solve for the new volatility sigma.
func updateVolatility(sigma, phi, v, delta float64) float64 {
	a := math.Log(sigma * sigma)
	f := func(x float64) float64 {
		ex := math.Exp(x)
		d2 := delta * delta
		p2 := phi * phi
		num := ex * (d2 - p2 - v - ex)
		den := 2.0 * (p2 + v + ex) * (p2 + v + ex)
		return num/den - (x-a)/(Tau*Tau)
	}

	eps := 0.000001
	A := a
	var B float64
	if delta*delta > phi*phi+v {
		B = math.Log(delta*delta - phi*phi - v)
	} else {
		k := 1.0
		for f(a-k*Tau) < 0 {
			k++
		}
		B = a - k*Tau
	}

	fA := f(A)
	fB := f(B)

	for math.Abs(B-A) > eps {
		C := A + (A-B)*fA/(fB-fA)
		fC := f(C)
		if fC*fB < 0 {
			A = B
			fA = fB
		} else {
			fA /= 2.0
		}
		B = C
		fB = fC
	}

	return math.Exp(A / 2.0)
}
