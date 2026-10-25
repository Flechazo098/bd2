package pvp

import (
	"fmt"
	"math"

	"bd2server/internal/server/domain/matchmaking"
	"bd2server/internal/server/protocol/wire"
)

// battleStatsField carries the FBattle plugin's report on PvpBattleEnd and
// ColosseumBattleEnd. Official clients never send it.
const battleStatsField = 100

// BattleStats is the attacker-side report of how much of each team remained.
type BattleStats struct {
	AttackerHP, AttackerMaxHP, DefenderHP, DefenderMaxHP uint64
	AttackerAlive, DefenderAlive, Turns                  uint64
}

func parseBattleStats(request []byte, result uint64) (*BattleStats, error) {
	raw, found, err := wire.Bytes(request, battleStatsField)
	if err != nil || !found {
		return nil, err
	}
	values := make([]uint64, 7)
	for i := range values {
		if values[i], _, err = wire.Varint(raw, i+1); err != nil {
			return nil, err
		}
	}
	s := &BattleStats{values[0], values[1], values[2], values[3], values[4], values[5], values[6]}
	// Overkill and timeouts decouple health from the result, so only bounds
	// and a margin contradicting the result are rejected.
	if s.AttackerMaxHP == 0 || s.DefenderMaxHP == 0 || s.AttackerHP > s.AttackerMaxHP || s.DefenderHP > s.DefenderMaxHP || s.AttackerMaxHP > math.MaxInt64 || s.DefenderMaxHP > math.MaxInt64 ||
		s.AttackerAlive > 32 || s.DefenderAlive > 32 || result == 1 && s.AttackerAlive == 0 && s.DefenderAlive != 0 || result == 2 && s.DefenderAlive == 0 && s.AttackerAlive != 0 {
		return nil, fmt.Errorf("pvp: inconsistent battle statistics")
	}
	return s, nil
}

// Margin is the attacker's remaining-health share minus the defender's.
func (s *BattleStats) Margin() *float64 {
	if s == nil {
		return nil
	}
	m := float64(s.AttackerHP)/float64(s.AttackerMaxHP) - float64(s.DefenderHP)/float64(s.DefenderMaxHP)
	return &m
}

// Rating covariates. Player-specific: log official battle power (missing
// when the client sent none), log account experience and team size.
// Contest-specific: the attacker's side advantage against an automated
// defense and the first-attack order effect.
const (
	PlayerCovariates  = 3
	ContestCovariates = 2
)

func covariates(s PublishedSnapshot) []float64 {
	power := math.NaN()
	if s.Power > 0 {
		power = math.Log(float64(s.Power))
	}
	team, _ := messages(s.Snapshot.FullDeck, 1)
	return []float64{power, math.Log1p(float64(s.Profile.XP)) / 10, float64(len(team)) / 5}
}

// RatingContest describes attacker against defender. firstAttack follows
// BattleSettled.FirstAttack: 1 attacker, 2 defender, 0 not yet decided.
func RatingContest(attacker, defender PublishedSnapshot, firstAttack uint64) matchmaking.Contest {
	order := 0.0
	switch firstAttack {
	case 1:
		order = 1
	case 2:
		order = -1
	}
	return matchmaking.Contest{Own: covariates(attacker), Other: covariates(defender), Context: []float64{1, order}}
}

// RatingResult converts an accepted battle into the contest result.
func RatingResult(b BattleSettled) (matchmaking.Result, error) {
	score := map[uint64]float64{1: 1, 2: 0, 3: 0.5}
	s, ok := score[b.Result]
	if !ok {
		return matchmaking.Result{}, fmt.Errorf("pvp: invalid battle result")
	}
	return matchmaking.Result{Score: s, Margin: b.Stats.Margin()}, nil
}
