package matchmaking

import (
	"errors"
	"math"
)

// experienceCap bounds the experience covariate like TrueSkill 2's 200-game
// experience offset table.
const experienceCap = 200

// Contest is one comparison in the contextual Bradley–Terry model
// (Turner & Firth, "Bradley-Terry Models in R"):
//
//	logit P(own beats other) = µown − µother + Σ βr(xown,r − xother,r) + Σ δq zq
//
// Own and Other are player-specific covariates (equation 3); a NaN marks a
// missing value and drops that covariate from the comparison. Context holds
// contest-specific covariates such as order or "home" advantage (equation 6).
type Contest struct {
	Own, Other []float64
	Context    []float64
}

// Model holds the shared covariate coefficients of one competitive mode. The
// last player coefficient belongs to the experience covariate.
type Model struct {
	Player, Contest []float64
	Observations    uint64
}

// Result is a decided contest from the first player's point of view. Margin,
// when known, is the remaining-strength difference in [-1, 1].
type Result struct {
	Score  float64
	Margin *float64
}

// System combines Glicko-2 ratings with an online-fitted contextual model.
type System struct {
	Config
	PlayerFeatures, ContestFeatures int
	// MarginWeight is the likelihood weight of the victory-margin observation.
	MarginWeight float64
	// LearningRate, Regularization and Limit control the stochastic gradient
	// fit of the covariate coefficients on the Glicko-2 scale.
	LearningRate, Regularization, Limit float64
}

func NewSystem(config Config, playerFeatures, contestFeatures int) (System, error) {
	s := System{Config: config, PlayerFeatures: playerFeatures, ContestFeatures: contestFeatures, MarginWeight: 0.5, LearningRate: 0.05, Regularization: 0.001, Limit: 4}
	if err := config.validate(); err != nil {
		return System{}, err
	}
	if playerFeatures < 0 || contestFeatures < 0 {
		return System{}, errors.New("matchmaking: negative covariate count")
	}
	return s, nil
}

func (s System) NewModel() Model {
	return Model{Player: make([]float64, s.PlayerFeatures+1), Contest: make([]float64, s.ContestFeatures)}
}

func (s System) validModel(m Model) bool {
	if len(m.Player) != s.PlayerFeatures+1 || len(m.Contest) != s.ContestFeatures {
		return false
	}
	for _, v := range append(append([]float64(nil), m.Player...), m.Contest...) {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

func experience(games uint64) float64 {
	return math.Log1p(float64(min(games, experienceCap))) / math.Log1p(experienceCap)
}

// design returns the covariate differences that multiply each coefficient.
func (s System) design(c Contest, own, other Rating) (player, contest []float64, err error) {
	if len(c.Own) != s.PlayerFeatures || len(c.Other) != s.PlayerFeatures || len(c.Context) != s.ContestFeatures {
		return nil, nil, errors.New("matchmaking: covariate count mismatch")
	}
	player = make([]float64, s.PlayerFeatures+1)
	for i := range c.Own {
		if !math.IsNaN(c.Own[i]) && !math.IsNaN(c.Other[i]) {
			player[i] = c.Own[i] - c.Other[i]
		}
		if math.IsInf(player[i], 0) {
			return nil, nil, errors.New("matchmaking: infinite covariate")
		}
	}
	player[s.PlayerFeatures] = experience(own.Games) - experience(other.Games)
	for _, z := range c.Context {
		if math.IsNaN(z) || math.IsInf(z, 0) {
			return nil, nil, errors.New("matchmaking: invalid contest covariate")
		}
	}
	return player, c.Context, nil
}

func (s System) offset(m Model, c Contest, own, other Rating) (float64, []float64, []float64, error) {
	if !s.validModel(m) {
		return 0, nil, nil, errors.New("matchmaking: invalid model")
	}
	player, contest, err := s.design(c, own, other)
	if err != nil {
		return 0, nil, nil, err
	}
	var total float64
	for i, x := range player {
		total += m.Player[i] * x
	}
	for i, z := range contest {
		total += m.Contest[i] * z
	}
	return total, player, contest, nil
}

func sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }

// Predict returns P(own beats other), integrating over both rating
// uncertainties with Glicko's g(φ) attenuation.
func (s System) Predict(m Model, own, other Rating, c Contest, at uint64) (float64, error) {
	own, other = s.Age(own, at), s.Age(other, at)
	offset, _, _, err := s.offset(m, c, own, other)
	if err != nil {
		return 0, err
	}
	return sigmoid(g(math.Hypot(own.Phi, other.Phi)) * (own.Mu - other.Mu + offset)), nil
}

// Quality is 1 for an even contest and falls to 0 for a certain one.
func Quality(p float64) float64 { return 1 - math.Abs(2*p-1) }

// Settle rates both players from one contest and refits the covariates with
// the pre-contest ratings. Both ratings are aged to the contest time first.
func (s System) Settle(m Model, own, other Rating, c Contest, r Result, at uint64) (Model, Rating, Rating, error) {
	if !(r.Score == 0 || r.Score == 0.5 || r.Score == 1) || r.Margin != nil && !(*r.Margin >= -1 && *r.Margin <= 1) {
		return m, own, other, errors.New("matchmaking: invalid contest result")
	}
	own, other = s.Age(own, at), s.Age(other, at)
	offset, player, contest, err := s.offset(m, c, own, other)
	if err != nil {
		return m, own, other, err
	}
	type observation struct{ score, weight float64 }
	observations := []observation{{r.Score, 1}}
	if r.Margin != nil && s.MarginWeight > 0 {
		observations = append(observations, observation{(1 + *r.Margin) / 2, s.MarginWeight})
	}
	var forOwn, forOther []Outcome
	for _, o := range observations {
		forOwn = append(forOwn, Outcome{Opponent: other, Score: o.score, Offset: offset, Weight: o.weight})
		forOther = append(forOther, Outcome{Opponent: own, Score: 1 - o.score, Offset: -offset, Weight: o.weight})
	}
	nextOwn, err := s.Update(own, forOwn)
	if err != nil {
		return m, own, other, err
	}
	nextOther, err := s.Update(other, forOther)
	if err != nil {
		return m, own, other, err
	}
	nextOwn.Games, nextOwn.Updated = own.Games+1, at
	nextOther.Games, nextOther.Updated = other.Games+1, at

	// One stochastic gradient step on the weighted log-likelihood with respect
	// to the offset, whose partial derivatives are the covariate differences.
	k := g(math.Hypot(own.Phi, other.Phi))
	p := sigmoid(k * (own.Mu - other.Mu + offset))
	var gradient float64
	for _, o := range observations {
		gradient += o.weight * (o.score - p) * k
	}
	rate := s.LearningRate / math.Sqrt(1+float64(m.Observations)/1000)
	next := Model{Player: append([]float64(nil), m.Player...), Contest: append([]float64(nil), m.Contest...), Observations: m.Observations + 1}
	step := func(weights, design []float64) {
		for i := range weights {
			w := weights[i] + rate*(gradient*design[i]-s.Regularization*weights[i])
			weights[i] = math.Max(-s.Limit, math.Min(s.Limit, w))
		}
	}
	step(next.Player, player)
	step(next.Contest, contest)
	return next, nextOwn, nextOther, nil
}
