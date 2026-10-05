package gamedata

import (
	"bd2server/internal/server/wire"
	"os"
	"testing"
)

func group(drop, count uint64, rs []BattleReward, ws []uint64) []byte {
	b := wire.AppendVarint(nil, 2, drop)
	b = wire.AppendVarint(b, 1, count)
	for _, r := range rs {
		b = wire.AppendVarint(b, 6, r.Type)
		b = wire.AppendVarint(b, 5, r.ID)
		b = wire.AppendVarint(b, 4, r.Count)
	}
	for _, w := range ws {
		b = wire.AppendVarint(b, 8, w)
	}
	return b
}
func TestRewardGraphWeightedNestedAndEquipmentLeaves(t *testing.T) {
	g := &RewardGraph{boxes: map[uint64]uint64{1: 1, 2: 2}, groups: map[uint64][]byte{1: group(0, 1, []BattleReward{{9, 2, 1}, {8, 8, 2}}, []uint64{1, 3}), 2: group(1, 0, []BattleReward{{10, 10, 1}, {11, 11, 1}}, []uint64{100, 100})}, sample: func(uint64) (uint64, error) { return 0, nil }}
	out, e := g.Resolve([]BattleReward{{9, 1, 2}})
	if e != nil || len(out) != 2 || out[0].Type != 10 || out[0].Count != 2 || out[1].Type != 11 {
		t.Fatalf("out=%v e=%v", out, e)
	}
	g.SetSampler(func(n uint64) (uint64, error) { return n - 1, nil })
	out, e = g.Resolve([]BattleReward{{9, 1, 1}})
	if e != nil || len(out) != 1 || out[0].Type != 8 || out[0].Count != 2 {
		t.Fatalf("out=%v e=%v", out, e)
	}
}
func TestRewardGraphCycleBadRandomAndOverflow(t *testing.T) {
	g := &RewardGraph{boxes: map[uint64]uint64{1: 1}, groups: map[uint64][]byte{1: group(1, 0, []BattleReward{{9, 1, 1}}, nil)}}
	if _, e := g.Resolve([]BattleReward{{9, 1, 1}}); e == nil {
		t.Fatal("cycle accepted")
	}
	g.groups[1] = group(0, 1, []BattleReward{{8, 1, 1}}, []uint64{1})
	g.sample = func(n uint64) (uint64, error) { return n, nil }
	if _, e := g.Resolve([]BattleReward{{9, 1, 1}}); e == nil {
		t.Fatal("bad random accepted")
	}
	if _, e := g.Resolve([]BattleReward{{8, 1, 2147483647}, {8, 1, 1}}); e == nil {
		t.Fatal("overflow accepted")
	}
}
func TestRewardGraphInstalledLoad(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not set")
	}
	g, e := LoadRewardGraph(root, "20260923193640")
	if e != nil {
		t.Fatal(e)
	}
	if len(g.boxes) == 0 || len(g.groups) == 0 {
		t.Fatal("empty graph")
	}
}
