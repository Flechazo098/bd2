package monsterhunt

import (
	"bd2server/internal/server/design/gamedata"
)

// Daily payout is the best reward achieved that UTC day. Improvements pay
// only positive per-item differences from the already paid level.
func dailyDifference(d *gamedata.MonsterHunt, previous, next uint64) []gamedata.BattleReward {
	paid := map[[2]uint64]uint64{}
	for _, r := range d.Rewards[previous].Daily {
		paid[[2]uint64{r.Type, r.ID}] += r.Count
	}
	var out []gamedata.BattleReward
	for _, r := range d.Rewards[next].Daily {
		n := paid[[2]uint64{r.Type, r.ID}]
		if r.Count > n {
			r.Count -= n
			out = append(out, r)
		}
	}
	return out
}

// This server has one persistent account state: a submitted participant ranks
// first among the local participants. No official leaderboard is imported.
func (s *Service) score(u user) float64 {
	progress := float64(0)
	if u.HighestHP > 0 {
		progress = float64(u.HighestHP-u.StartHP) / float64(u.HighestHP) * 10000
	}
	return float64(u.ClearLevel*1000000) + progress
}

func (s *Service) rankRewards(d *gamedata.MonsterHunt, group uint64) []gamedata.BattleReward {
	var selected *gamedata.MonsterHuntRankReward
	for _, r := range d.Ranks[group] {
		threshold := float64(1)
		if r.Type == 1 {
			threshold = 100
		}
		if r.Ranking >= threshold && (selected == nil || r.Type < selected.Type || r.Type == selected.Type && r.Ranking < selected.Ranking) {
			selected = &r
		}
	}
	if selected == nil {
		return nil
	}
	return selected.Rewards
}
