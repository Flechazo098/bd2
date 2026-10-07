package eventplay

import (
	"bd2server/internal/server/design/gamedata"
)

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
