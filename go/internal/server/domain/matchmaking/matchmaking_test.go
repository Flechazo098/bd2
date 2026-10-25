package matchmaking

import (
	"math"
	"testing"
	"time"
)

func glicko(c Config, rating, deviation float64) Rating {
	return Rating{Mu: (rating - c.InitialRating) / glickoScale, Phi: deviation / glickoScale, Sigma: c.InitialVolatility}
}

// Glickman's worked example: a 1500/200 player beats 1400/30 and loses to
// 1550/100 and 1700/300 with τ = 0.5, ending at 1464.06/151.52, σ' 0.05999.
func TestGlicko2MatchesGlickmanExample(t *testing.T) {
	c := DefaultConfig
	games := []Outcome{
		{Opponent: glicko(c, 1400, 30), Score: 1, Weight: 1},
		{Opponent: glicko(c, 1550, 100), Score: 0, Weight: 1},
		{Opponent: glicko(c, 1700, 300), Score: 0, Weight: 1},
	}
	r, err := c.Update(glicko(c, 1500, 200), games)
	if err != nil {
		t.Fatal(err)
	}
	rating, deviation := c.Display(r)
	if math.Abs(rating-1464.06) > 0.01 || math.Abs(deviation-151.52) > 0.01 || math.Abs(r.Sigma-0.05999) > 0.00001 {
		t.Fatalf("rating %.2f deviation %.2f volatility %.5f", rating, deviation, r.Sigma)
	}
}

func TestIdleRatingGrowsUncertaintyUpToNewPlayer(t *testing.T) {
	c := DefaultConfig
	r := glicko(c, 1500, 50)
	r.Updated = 1
	day := uint64(c.Period.Milliseconds())
	_, after := c.Display(c.Age(r, 1+100*day))
	want := math.Sqrt(50*50/(glickoScale*glickoScale)+100*0.06*0.06) * glickoScale
	if math.Abs(after-want) > 1e-9 {
		t.Fatalf("100 idle periods: deviation %.4f, want %.4f", after, want)
	}
	if _, capped := c.Display(c.Age(r, 1+uint64(100*365*24*time.Hour/time.Millisecond))); capped != c.InitialDeviation {
		t.Fatalf("a century idle: deviation %.4f, want the new-player %.0f", capped, c.InitialDeviation)
	}
}

// A covariate that decides contests regardless of who plays (the same player
// fields different decks) is learned, and then predicts the stronger side even
// between equally rated players. A covariate fixed per player would instead be
// absorbed by that player's own rating.
func TestContextualModelLearnsDecisiveCovariate(t *testing.T) {
	s, err := NewSystem(DefaultConfig, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	m := s.NewModel()
	players := []Rating{s.New(), s.New(), s.New(), s.New()}
	for round := range 400 {
		a, b := round%4, (round+1+round/4)%4
		strengthA, strengthB := float64(round%3), float64((round/3)%4)
		if a == b || strengthA == strengthB {
			continue
		}
		score := 0.0
		if strengthA > strengthB {
			score = 1
		}
		m, players[a], players[b], err = s.Settle(m, players[a], players[b], Contest{Own: []float64{strengthA}, Other: []float64{strengthB}}, Result{Score: score}, uint64(round+1))
		if err != nil {
			t.Fatal(err)
		}
	}
	if m.Player[0] <= 0.5 {
		t.Fatalf("decisive covariate coefficient %.3f", m.Player[0])
	}
	fresh := s.New()
	p, err := s.Predict(m, fresh, fresh, Contest{Own: []float64{3}, Other: []float64{0}}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if p <= 0.8 {
		t.Fatalf("stronger side predicted to win with %.3f", p)
	}
	missing, err := s.Predict(m, fresh, fresh, Contest{Own: []float64{math.NaN()}, Other: []float64{0}}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if missing != 0.5 {
		t.Fatalf("missing covariate still shifted the prediction to %.3f", missing)
	}
}

// A narrow win moves ratings less than a flawless one.
func TestVictoryMarginScalesRatingChange(t *testing.T) {
	s, err := NewSystem(DefaultConfig, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	narrow, flawless := -0.9, 1.0
	_, close, _, err := s.Settle(s.NewModel(), s.New(), s.New(), Contest{}, Result{Score: 1, Margin: &narrow}, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, clean, _, err := s.Settle(s.NewModel(), s.New(), s.New(), Contest{}, Result{Score: 1, Margin: &flawless}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !(close.Mu > 0 && clean.Mu > close.Mu) {
		t.Fatalf("narrow win µ %.4f, flawless win µ %.4f", close.Mu, clean.Mu)
	}
}
