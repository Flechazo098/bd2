package eventplay

import (
	"bd2server/internal/server/design/gamedata"
	"sort"
)

func rewardDifference(old, next []gamedata.BattleReward) []gamedata.BattleReward {
	paid := map[[2]uint64]uint64{}
	earned := map[[2]uint64]uint64{}
	for _, r := range old {
		paid[[2]uint64{r.Type, r.ID}] += r.Count
	}
	for _, r := range next {
		earned[[2]uint64{r.Type, r.ID}] += r.Count
	}
	keys := make([][2]uint64, 0, len(earned))
	for k := range earned {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	var out []gamedata.BattleReward
	for _, k := range keys {
		if earned[k] > paid[k] {
			out = append(out, gamedata.BattleReward{Type: k[0], ID: k[1], Count: earned[k] - paid[k]})
		}
	}
	return out
}
