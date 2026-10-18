package monsterhunt

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/protocol/wire"
	"fmt"
	"math"
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

func (s *Service) score(u user) (float64, error) {
	d, err := s.load(u.Hunt)
	if err != nil {
		return 0, err
	}
	if d.RankingVersion == 1 {
		return float64(u.RankLevel*100000000000000 + u.RankDamage), nil
	}
	if u.RankMaxHP == 0 {
		return 0, nil
	}
	progress := math.Floor(float64(u.RankMaxHP-u.RankRemainingHP) / float64(u.RankMaxHP) * 1000000)
	return float64(u.RankLevel*1000000) + progress, nil
}

func updateRank(u *user, d *gamedata.MonsterHunt, level, totalDamage, remaining, maxHP uint64) error {
	if d == nil || d.RankingVersion > 1 || level == 0 || maxHP == 0 || remaining > maxHP || totalDamage >= 100000000000000 || level > math.MaxInt64/100000000000000 {
		return fmt.Errorf("monsterhunt: ranking value outside client encoding range")
	}
	if d.RankingVersion == 1 && remaining != 0 {
		return nil
	}
	better := level > u.RankLevel
	if level == u.RankLevel {
		if d.RankingVersion == 1 {
			better = totalDamage > u.RankDamage
		} else {
			better = u.RankMaxHP == 0 || float64(remaining)/float64(maxHP) < float64(u.RankRemainingHP)/float64(u.RankMaxHP)
		}
	}
	if better {
		u.RankLevel, u.RankDamage, u.RankRemainingHP, u.RankMaxHP = level, totalDamage, remaining, maxHP
	}
	return nil
}

func monsterHuntAttackDamage(request []byte) (uint64, error) {
	statistics, found, err := wire.Bytes(request, 10)
	if err != nil || !found {
		return 0, fmt.Errorf("monsterhunt: missing battle statistics")
	}
	entries, err := messages(statistics, 1)
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, entry := range entries {
		damage, err := scalar(entry, 2)
		if err != nil || damage >= 100000000000000 || total >= 100000000000000-damage {
			return 0, fmt.Errorf("monsterhunt: attack damage outside ranking range")
		}
		total += damage
	}
	return total, nil
}

func (s *Service) rankRewards(d *gamedata.MonsterHunt, group uint64, rank RankEntry) []gamedata.BattleReward {
	var selected *gamedata.MonsterHuntRankReward
	for _, r := range d.Ranks[group] {
		eligible := r.Type == 0 && float64(rank.Rank) <= r.Ranking || r.Type == 1 && rank.TopPercent < r.Ranking+0.1
		if rank.Rank != 0 && eligible && (selected == nil || r.Type < selected.Type || r.Type == selected.Type && r.Ranking < selected.Ranking) {
			selected = &r
		}
	}
	if selected == nil {
		return nil
	}
	return selected.Rewards
}

func (s *Service) pendingSeasonReward() (Season, bool) {
	var next Season
	now := uint64(s.now().UnixMilli())
	for _, season := range s.seasons {
		u, ok := s.state.Users[fmt.Sprint(season.ID)]
		if !ok || !u.Played || u.RankLevel == 0 || u.Claimed || now < season.End || now <= season.Calculate {
			continue
		}
		if next.ID == 0 || season.Calculate < next.Calculate || season.Calculate == next.Calculate && season.ID < next.ID {
			next = season
		}
	}
	return next, next.ID != 0
}
