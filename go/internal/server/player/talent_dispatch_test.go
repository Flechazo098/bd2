package player

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"os"
	"testing"
	"time"
)

type dispatchEconomyFake struct {
	seen   map[string]bool
	grants int
}

func (e *dispatchEconomyFake) Apply(id string, c, r []gamedata.Reward) ([]byte, error) {
	if !e.seen[id] {
		e.seen[id] = true
		e.grants++
	}
	return wire.AppendBytes(nil, 1, wire.AppendVarint(nil, 3, 4)), nil
}
func TestTalentDispatchReadyRestartRewardReplayAndPrefabExclusion(t *testing.T) {
	root := "../../../../data/resources/GameData"
	if _, e := os.Stat(root); e != nil {
		t.Skip("installed GameData missing")
	}
	d, e := gamedata.LoadTalentDispatchDesign(root, "20260923193640")
	if e != nil {
		t.Fatal(e)
	}
	st := stateio.NewMemory()
	eco := &dispatchEconomyFake{seen: map[string]bool{}}
	s, e := OpenTalentDispatch(st, d, eco)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.BeginSession("dispatch")
	s.draw = func(uint64) (uint64, error) { return 0, nil }
	rule := gamedata.TalentUseRule{Class: 18, Values: []float64{1, 2}}
	char := Character{InvenIndex: 99}
	if _, e = s.Start("foreign", char, rule, []uint64{11}); e == nil {
		t.Fatal("foreign dispatch accepted")
	}
	if _, e = s.Start("duplicate-prefab", char, rule, []uint64{1, 2}); e == nil {
		t.Fatal("duplicate prefab accepted")
	}
	b, e := s.Start("start1", char, rule, []uint64{1})
	if e != nil {
		t.Fatal(e)
	}
	b2, e := s.Start("start1", char, rule, []uint64{1})
	if e != nil || !bytes.Equal(b, b2) {
		t.Fatal("start replay", e)
	}
	if _, e = s.Start("start2", char, rule, []uint64{2}); e == nil {
		t.Fatal("active prefab reused")
	}
	req := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 1)
	if _, _, _, e = s.Handle("/DispatchReward", req); e == nil {
		t.Fatal("claimed before end")
	}
	now = d[1].EndTime(now)
	reopened, e := OpenTalentDispatch(st, d, eco)
	if e != nil {
		t.Fatal(e)
	}
	reopened.now = s.now
	reopened.BeginSession("dispatch")
	_, reward, _, e := reopened.Handle("/DispatchReward", req)
	if e != nil {
		t.Fatal(e)
	}
	_, replay, _, e := reopened.Handle("/DispatchReward", req)
	if e != nil || !bytes.Equal(reward, replay) || eco.grants != 1 {
		t.Fatal("reward replay", e, eco.grants)
	}
	newClaim := wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, 1)
	_, again, _, e := reopened.Handle("/DispatchReward", newClaim)
	if e != nil || len(again) != 0 || eco.grants != 1 {
		t.Fatal("new sequence emitted already claimed item deltas")
	}
	_, info, _, e := reopened.Handle("/DispatchInfo", wire.AppendVarint(nil, 1, 2))
	if e != nil || len(info) != 0 {
		t.Fatal("claimed dispatch still active", e)
	}
	if _, e = reopened.Start("start3", char, rule, []uint64{2}); e != nil {
		t.Fatal("claimed prefab cannot restart", e)
	}
}
