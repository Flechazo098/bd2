package gamedata

import (
	"bd2server/internal/server/wire"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestGrantedOpenWrappersAndDirectInventoryBoxes(t *testing.T) {
	g := &RewardGraph{
		boxes:  map[uint64]uint64{1: 1, 2: 2, 3: 3},
		direct: map[uint64]bool{1: false, 2: true, 3: false},
		groups: map[uint64][]byte{
			1: group(1, 0, []BattleReward{{9, 2, 2}, {9, 3, 1}}, []uint64{100, 100}),
			2: group(1, 0, []BattleReward{{8, 1000, 5}}, []uint64{100}),
			3: group(1, 0, []BattleReward{{3, 0, 10}}, []uint64{100}),
		},
		sample: func(uint64) (uint64, error) { return 0, nil },
	}
	got, err := g.ResolveGranted([]BattleReward{{9, 1, 2}, {9, 2, 1}})
	want := []BattleReward{{3, 0, 20}, {9, 2, 5}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("granted=%+v want=%+v err=%v", got, want, err)
	}
	// Explicitly opening the root does not consume a nested inventory box.
	got, err = g.Resolve([]BattleReward{{9, 1, 1}})
	if err != nil || !reflect.DeepEqual(got, []BattleReward{{3, 0, 10}, {9, 2, 2}}) {
		t.Fatalf("open nested=%+v err=%v", got, err)
	}
	got, err = g.Resolve([]BattleReward{{9, 2, 1}})
	if err != nil || !reflect.DeepEqual(got, []BattleReward{{8, 1000, 5}}) {
		t.Fatalf("manual direct root=%+v err=%v", got, err)
	}
	if _, err := g.ResolveGranted([]BattleReward{{9, 999, 1}}); err == nil {
		t.Fatal("unknown box accepted")
	}
	g.boxes[10], g.direct[10] = 10, true
	g.special = map[uint64]bool{10: true}
	if got, err := g.ResolveGranted([]BattleReward{{9, 10, 1}}); err != nil || !reflect.DeepEqual(got, []BattleReward{{9, 10, 1}}) {
		t.Fatalf("special box grant=%+v err=%v", got, err)
	}
	if _, err := g.Resolve([]BattleReward{{9, 10, 1}}); err == nil {
		t.Fatal("special box used generic random opening")
	}
	g.sample = func(uint64) (uint64, error) { return 0, errors.New("sample failed") }
	if out, err := g.ResolveGranted([]BattleReward{{9, 1, 1}}); err == nil || out != nil {
		t.Fatal("failed opening returned partial rewards")
	}
}

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
	g := &RewardGraph{boxes: map[uint64]uint64{1: 1, 2: 2}, direct: map[uint64]bool{1: false, 2: false}, groups: map[uint64][]byte{1: group(0, 1, []BattleReward{{9, 2, 1}, {8, 8, 2}}, []uint64{1, 3}), 2: group(1, 0, []BattleReward{{10, 10, 1}, {11, 11, 1}}, []uint64{100, 100})}, sample: func(uint64) (uint64, error) { return 0, nil }}
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
	g := &RewardGraph{boxes: map[uint64]uint64{1: 1}, direct: map[uint64]bool{1: false}, groups: map[uint64][]byte{1: group(1, 0, []BattleReward{{9, 1, 1}}, nil)}}
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
	// Real 2.35.10 pass levels 5..20 BASIC/PREMIUM: OPEN wrappers must yield
	// actual wallet/material leaves, while nested DIRECT boxes remain owned.
	var rewards []BattleReward
	for id := uint64(532657); id <= 532672; id++ {
		rewards = append(rewards, BattleReward{9, id, 1})
	}
	for id := uint64(532677); id <= 532692; id++ {
		rewards = append(rewards, BattleReward{9, id, 1})
	}
	out, err := g.ResolveGranted(rewards)
	if err != nil {
		t.Fatal(err)
	}
	want := []BattleReward{{3, 0, 600}, {4, 0, 70000}, {8, 7, 40}, {8, 8, 35}, {8, 11, 1}, {8, 12, 2}, {8, 13, 8}, {8, 14, 1}, {8, 1000, 16}, {8, 1002, 10}, {8, 1003, 1}, {9, 504143, 13}, {9, 504145, 23}, {12, 0, 1500}}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("real pass material grant=%+v want=%+v", out, want)
	}
}
