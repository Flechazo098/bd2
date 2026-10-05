package eventplay

import (
	"bd2server/internal/server/gamedata"
	"fmt"
	"strconv"
)

func (s *Service) upgrade(path string, req []byte, uid uint64, next *snapshot, key string) ([]byte, error) {
	prefix := fmt.Sprintf("%d:", uid)
	var costs, rewards []gamedata.Reward
	if path == "/MiniGameSurvivalCharUpgrade" {
		id, target := num(req, 2), num(req, 3)
		k := prefix + strconv.FormatUint(id, 10)
		if target == 0 || target != next.Upgrades[k]+1 {
			return nil, fmt.Errorf("eventplay: upgrade level is not next")
		}
		var row []byte
		for _, r := range s.design.Rows("FieldMiniGameUpgradeTable", 1, id) {
			if num(r, 2) == target {
				row = r
				break
			}
		}
		if row == nil {
			return nil, fmt.Errorf("eventplay: upgrade design missing")
		}
		costs = []gamedata.Reward{{Type: 43, Count: num(row, 3)}}
		if _, e := s.economy.Apply("eventplay:"+key, costs, nil); e != nil {
			return nil, e
		}
		next.Upgrades[k] = target
		return nil, nil
	}
	for _, r := range s.design.Tables["FieldMiniGameUpgradeTable"] {
		id, level := num(r, 1), num(r, 2)
		if next.Upgrades[prefix+strconv.FormatUint(id, 10)] >= level {
			rewards = append(rewards, gamedata.Reward{Type: 43, Count: num(r, 3)})
		}
	}
	bundle, e := s.economy.Apply("eventplay:"+key, nil, rewards)
	if e != nil {
		return nil, e
	}
	for k := range next.Upgrades {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(next.Upgrades, k)
		}
	}
	return bundle, nil
}
func rewardDifference(old, next []gamedata.BattleReward) []gamedata.BattleReward {
	paid := map[[2]uint64]uint64{}
	for _, r := range old {
		paid[[2]uint64{r.Type, r.ID}] += r.Count
	}
	var out []gamedata.BattleReward
	for _, r := range next {
		n := paid[[2]uint64{r.Type, r.ID}]
		if r.Count > n {
			r.Count -= n
			out = append(out, r)
		}
	}
	return out
}
