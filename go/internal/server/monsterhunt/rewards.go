package monsterhunt

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
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
func (s *Service) rankWire(u user) []byte {
	b := wire.AppendVarint(nil, 7, 1)
	b = wire.AppendDouble(b, 8, s.score(u))
	return wire.AppendDouble(b, 10, 100)
}
func (s *Service) rankRewards(d *gamedata.MonsterHunt, group uint64) []gamedata.BattleReward {
	var selected *gamedata.MonsterHuntRankReward
	for _, r := range d.Ranks[group] {
		r := r
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
func (s *Service) grant(identity string, rewards []gamedata.BattleReward) ([]byte, error) {
	if s.rewardGrant != nil {
		rs := make([]gamedata.Reward, len(rewards))
		for i, r := range rewards {
			rs[i] = gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count}
		}
		return s.rewardGrant(identity, rs)
	}
	var currency []gamedata.Reward
	var stack []gamedata.BattleReward
	for _, r := range rewards {
		switch r.Type {
		case 2, 3, 4, 12, 20:
			currency = append(currency, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count})
		default:
			stack = append(stack, r)
		}
	}
	if _, e := s.wallet.GrantQuestOnce(identity+":currency", currency); e != nil {
		return nil, e
	}
	items, e := s.inventory.GrantOnce(identity+":items", stack)
	if e != nil {
		return nil, e
	}
	if len(items) == 0 {
		items = s.inventory.GrantedItems(identity + ":items")
	}
	var bundle []byte
	for _, item := range items {
		bundle = wire.AppendBytes(bundle, 1, player.ItemWire(item))
		v := wire.AppendVarint(nil, 2, item.ID)
		v = wire.AppendVarint(v, 3, item.Type)
		v = wire.AppendVarint(v, 4, item.Count)
		bundle = wire.AppendBytes(bundle, 6, v)
	}
	for _, r := range currency {
		v := wire.AppendVarint(nil, 3, r.Type)
		v = wire.AppendVarint(v, 4, r.Count)
		bundle = wire.AppendBytes(bundle, 1, v)
	}
	return bundle, nil
}
