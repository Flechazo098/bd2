package gamedata

import (
	"fmt"
	"testing"
)

func TestDispatchWeightedNestedRewards(t *testing.T) {
	leaf := &dispatchGroup{Drop: 1, Entries: []dispatchEntry{{Reward: BattleReward{Type: 8, ID: 44, Count: 3}}}}
	weighted := &dispatchGroup{Drop: 0, Count: 2, Entries: []dispatchEntry{{Reward: BattleReward{Type: 9, Count: 1}, Weight: 90, Child: leaf}, {Reward: BattleReward{Type: 4, Count: 5}, Weight: 10}}}
	d := &DispatchDesign{Rewards: []BattleReward{{Type: 9, ID: 7, Count: 1}}, boxes: map[uint64]*dispatchGroup{7: weighted}}
	draws := 0
	r, err := d.Roll(2, func(n uint64) (uint64, error) {
		if n != 100 {
			return 0, fmt.Errorf("limit %d", n)
		}
		draws++
		if draws%2 == 0 {
			return 95, nil
		}
		return 0, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	totals := map[uint64]uint64{}
	for _, v := range r {
		totals[v.Type] += v.Count
	}
	if totals[8] != 6 || totals[4] != 10 || draws != 4 {
		t.Fatalf("rewards %+v draws %d", r, draws)
	}
	if _, err = d.Roll(1, func(n uint64) (uint64, error) { return n, nil }); err == nil {
		t.Fatal("out of range RNG accepted")
	}
}
func TestDispatchRewardOverflow(t *testing.T) {
	d := &DispatchDesign{Rewards: []BattleReward{{Type: 4, Count: 2147483647}}}
	if _, err := d.Roll(2, nil); err == nil {
		t.Fatal("overflow accepted")
	}
}
