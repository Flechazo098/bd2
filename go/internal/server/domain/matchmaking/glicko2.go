// Package matchmaking rates competitive players with Glicko-2 and predicts
// contest outcomes with a contextual Bradley–Terry model. Ratings and the
// model are server-defined; official score and reward rules stay elsewhere.
package matchmaking

import (
	"errors"
	"math"
	"time"
)

// glickoScale converts between the Glicko and Glicko-2 rating scales.
const glickoScale = 173.7178

// Config holds the Glicko-2 system constants (Glickman, "Example of the
// Glicko-2 system", step 1) on the original Glicko scale.
type Config struct {
	InitialRating, InitialDeviation, InitialVolatility, Tau float64
	// Period is the rating period: an idle player's deviation grows by one
	// volatility step per elapsed period, never past InitialDeviation.
	Period time.Duration
}

var DefaultConfig = Config{InitialRating: 1500, InitialDeviation: 350, InitialVolatility: 0.06, Tau: 0.5, Period: 24 * time.Hour}

// Rating is a player's skill belief on the Glicko-2 scale.
type Rating struct {
	Mu, Phi, Sigma float64
	Games          uint64
	// Updated is the Unix millisecond time of the last rated contest.
	Updated uint64
}

// Outcome is one observation within a rating period. Offset is the contextual
// logit shift from the rated player's point of view, in Glicko-2 units.
// Weight scales its likelihood: 1 for a decided contest, less for auxiliary
// evidence such as the victory margin (TrueSkill 2, section 8).
type Outcome struct {
	Opponent Rating
	Score    float64
	Offset   float64
	Weight   float64
}

func (c Config) validate() error {
	if c.InitialDeviation <= 0 || c.InitialVolatility <= 0 || c.Tau <= 0 || c.Period <= 0 {
		return errors.New("matchmaking: invalid Glicko-2 constants")
	}
	return nil
}

func (c Config) New() Rating {
	return Rating{Mu: 0, Phi: c.InitialDeviation / glickoScale, Sigma: c.InitialVolatility}
}

// Display returns the rating and deviation on the original Glicko scale.
func (c Config) Display(r Rating) (rating, deviation float64) {
	return glickoScale*r.Mu + c.InitialRating, glickoScale * r.Phi
}

// Age applies step 6 for every rating period elapsed since the last contest.
func (c Config) Age(r Rating, at uint64) Rating {
	if r.Updated == 0 || at <= r.Updated {
		return r
	}
	periods := float64(at-r.Updated) / float64(c.Period.Milliseconds())
	r.Phi = math.Min(math.Sqrt(r.Phi*r.Phi+r.Sigma*r.Sigma*periods), c.InitialDeviation/glickoScale)
	return r
}

func g(phi float64) float64 { return 1 / math.Sqrt(1+3*phi*phi/(math.Pi*math.Pi)) }

// expectation is E(µ, µj, φj) shifted by the contextual offset.
func expectation(r Rating, o Outcome) float64 {
	return 1 / (1 + math.Exp(-g(o.Opponent.Phi)*(r.Mu-o.Opponent.Mu+o.Offset)))
}

// Update applies steps 3–7 to one player's rating period.
func (c Config) Update(r Rating, games []Outcome) (Rating, error) {
	if len(games) == 0 {
		return r, errors.New("matchmaking: a rating period needs at least one contest")
	}
	var inverseV, improvement float64
	for _, o := range games {
		if !(o.Score >= 0 && o.Score <= 1 && o.Weight > 0 && o.Weight <= 1) || math.IsNaN(o.Offset) || math.IsInf(o.Offset, 0) {
			return r, errors.New("matchmaking: invalid contest outcome")
		}
		e := expectation(r, o)
		gj := g(o.Opponent.Phi)
		inverseV += o.Weight * gj * gj * e * (1 - e)
		improvement += o.Weight * gj * (o.Score - e)
	}
	v := 1 / inverseV
	delta := v * improvement
	sigma := c.volatility(r, delta, v)
	phiStar := math.Sqrt(r.Phi*r.Phi + sigma*sigma)
	phi := 1 / math.Sqrt(1/(phiStar*phiStar)+1/v)
	r.Mu += phi * phi * improvement
	r.Phi = phi
	r.Sigma = sigma
	return r, nil
}

// volatility is step 5, the Illinois-algorithm root of f(x).
func (c Config) volatility(r Rating, delta, v float64) float64 {
	const epsilon = 1e-6
	a := math.Log(r.Sigma * r.Sigma)
	phi2 := r.Phi * r.Phi
	f := func(x float64) float64 {
		ex := math.Exp(x)
		d := phi2 + v + ex
		return ex*(delta*delta-phi2-v-ex)/(2*d*d) - (x-a)/(c.Tau*c.Tau)
	}
	A := a
	var B float64
	if delta*delta > phi2+v {
		B = math.Log(delta*delta - phi2 - v)
	} else {
		k := 1.0
		for f(a-k*c.Tau) < 0 {
			k++
		}
		B = a - k*c.Tau
	}
	fA, fB := f(A), f(B)
	for math.Abs(B-A) > epsilon {
		C := A + (A-B)*fA/(fB-fA)
		fC := f(C)
		if fC*fB <= 0 {
			A, fA = B, fB
		} else {
			fA /= 2
		}
		B, fB = C, fC
	}
	return math.Exp(A / 2)
}
