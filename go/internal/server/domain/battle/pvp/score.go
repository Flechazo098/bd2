package pvp

import (
	"bd2server/internal/server/design/gamedata"
	"fmt"
	"math"
)

// LocalScorePolicy is this server's explicit scoring rule. ELO and Colosseum
// formulas are local policies; their constants and reward rows come from GD.
type LocalScorePolicy struct{ Design *gamedata.PvpDesign }

func NewLocalScorePolicy(d *gamedata.PvpDesign) (*LocalScorePolicy, error) {
	if d == nil || d.Mirror.CorrectionFactor == 0 || d.Colosseum.VariableChange == 0 || d.Colosseum.MinimumChange == 0 || d.Colosseum.MaximumChange < d.Colosseum.MinimumChange {
		return nil, fmt.Errorf("pvp: invalid local scoring constants")
	}
	return &LocalScorePolicy{Design: d}, nil
}

func (p *LocalScorePolicy) Resolve(in ScoreInput) (ScoreChange, error) {
	if in.Result < 1 || in.Result > 3 || in.Multiplier == 0 || in.Multiplier > p.Design.Mirror.BoostMaximum && in.Mode == Mirror {
		return ScoreChange{}, fmt.Errorf("pvp: invalid score input")
	}
	var result ScoreChange
	switch in.Mode {
	case Mirror:
		attacker, err := p.Design.MirrorRank(in.Season, in.AttackerVP, in.AttackerRank)
		if err != nil {
			return result, err
		}
		defender, err := p.Design.MirrorRank(in.Season, in.DefenderVP, in.DefenderRank)
		if err != nil {
			return result, err
		}
		if in.Result == 1 {
			result.Attacker = int64(attacker.WinPoint)
			result.Defender = -int64(defender.LosePoint)
			result.Rewards = attacker.WinRewards
		}
		if in.Result == 2 {
			result.Attacker = -int64(attacker.LosePoint)
			result.Defender = int64(defender.WinPoint)
			result.Rewards = attacker.LoseRewards
		}
		if in.Result != 3 {
			actual := float64(0)
			if in.Result == 1 {
				actual = 1
			}
			expected := 1 / (1 + math.Pow(10, (float64(in.DefenderVP)-float64(in.AttackerVP))/400))
			change := int64(math.Round(float64(p.Design.Mirror.CorrectionFactor) * (actual - expected)))
			if attacker.ELO {
				result.Attacker = change
			}
			if defender.ELO {
				result.Defender = -change
			}
		}
	case Colosseum:
		d := p.Design.Colosseum
		if in.Result != 3 {
			gap := float64(in.DefenderVP) - float64(in.AttackerVP)
			if in.Result == 2 {
				gap = -gap
			}
			change := math.Round(float64(d.DefaultChange) + gap/float64(d.VariableChange))
			change = math.Max(float64(d.MinimumChange), math.Min(float64(d.MaximumChange), change))
			result.Attacker = int64(change)
			if in.Result == 2 {
				result.Attacker = -result.Attacker
			}
			result.Defender = -result.Attacker
		}
	default:
		return result, fmt.Errorf("pvp: unsupported scoring mode")
	}
	result.Attacker *= int64(in.Multiplier)
	result.Defender *= int64(in.Multiplier)
	result.Rewards = append([]gamedata.BattleReward(nil), result.Rewards...)
	for i := range result.Rewards {
		result.Rewards[i].Count *= in.Multiplier
	}
	return result, nil
}
