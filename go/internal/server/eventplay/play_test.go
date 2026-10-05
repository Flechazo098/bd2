package eventplay

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func nums(b []byte, n int) ([]uint64, error) { return list(b, n) }

type econ struct{ cost, granted uint64 }

func (e *econ) Apply(_ string, c, r []gamedata.Reward) ([]byte, error) {
	for _, x := range c {
		e.cost += x.Count
	}
	for _, x := range r {
		e.granted += x.Count
	}
	return wire.AppendVarint(nil, 1, 99), nil
}
func makeService(t *testing.T) (*Service, *econ) {
	t.Helper()
	reg := events.NewRegistry()
	if e := reg.Replace([]events.Schedule{{UID: 7, Type: 9, ID: 1, Start: 1, End: 9999999}, {UID: 8, Type: 10, ID: 1, Start: 1, End: 9999999}, {UID: 9, Type: 11, ID: 1, Start: 1, End: 9999999}}); e != nil {
		t.Fatal(e)
	}
	e := &econ{}
	s := &Service{store: stateio.NewMemory(), registry: reg, economy: e, now: func() time.Time { return time.UnixMilli(100) }, design: &gamedata.EventPlayCatalog{Tables: map[string][][]byte{}}, state: snapshot{1, map[string]record{}, map[string]Run{}, map[string]reply{}, map[string]bool{}, map[string]uint64{}}}
	return s, e
}
func TestBattleRewardFieldFiveClearInfoSixteenAndRetry(t *testing.T) {
	s, e := makeService(t)
	row := wire.AppendVarint(nil, 4, 1)
	row = wire.AppendVarint(row, 5, 1)
	row = wire.AppendVarint(row, 1, 88)
	row = wire.AppendVarint(row, 3, 1)
	row = wire.AppendVarint(row, 9, 4)
	row = wire.AppendVarint(row, 8, 50)
	s.design.Tables["PackEventBattleTable"] = [][]byte{row}
	req := wire.AppendVarint(nil, 5, 17)
	req = wire.AppendVarint(req, 8, 1)
	req = wire.AppendVarint(req, 9, 1)
	req = wire.AppendVarint(req, 4, 88)
	if _, err := s.EnterBattle(req, "sid:1"); err != nil {
		t.Fatal(err)
	}
	end := wire.AppendVarint(nil, 2, 1)
	a, err := s.CompleteBattle(end, "sid:1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := wire.Bytes(a, 5); !ok {
		t.Fatal("reward_bundle absent field5")
	}
	if _, ok, _ := wire.Bytes(a, 3); ok {
		t.Fatal("bundle polluted CharInfo field3")
	}
	if _, ok, _ := wire.Bytes(a, 16); !ok {
		t.Fatal("clear info absent")
	}
	b, err := s.CompleteBattle(end, "sid:1")
	if err != nil || !bytes.Equal(a, b) || e.cost != 1 || e.granted != 50 {
		t.Fatalf("retry %v cost%d grant%d", err, e.cost, e.granted)
	}
}
func TestStoryClearPersistentOnceAndChangedRetry(t *testing.T) {
	s, e := makeService(t)
	r := wire.AppendVarint(nil, 1, 1)
	r = wire.AppendVarint(r, 2, 1)
	r = wire.AppendVarint(r, 11, 4)
	r = wire.AppendVarint(r, 9, 10)
	s.design.Tables["PackEventStoryTable"] = [][]byte{r}
	req := wire.AppendVarint(nil, 1, 1)
	req = wire.AppendVarint(req, 2, 8)
	req = wire.AppendVarint(req, 3, 1)
	req = wire.AppendVarint(req, 4, 1)
	if _, _, _, err := s.HandleSession("/PackEventStoryClear", req, "sid"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.HandleSession("/PackEventStoryClear", req, "sid"); err != nil {
		t.Fatal(err)
	}
	if e.granted != 10 {
		t.Fatal("duplicate story award")
	}
	bad := wire.AppendVarint(req, 9, 1)
	if _, _, _, err := s.HandleSession("/PackEventStoryClear", bad, "sid"); err == nil {
		t.Fatal("changed retry accepted")
	}
}
func TestScoreRewardUpgradeDifference(t *testing.T) {
	r := rewardDifference([]gamedata.BattleReward{{Type: 4, Count: 10}}, []gamedata.BattleReward{{Type: 4, Count: 15}})
	if len(r) != 1 || r[0].Count != 5 {
		t.Fatal(r)
	}
}

func TestEndScoreWireFieldsAcrossFamilies(t *testing.T) {
	s, _ := makeService(t)
	for _, tc := range []struct {
		family string
		field  int
		value  uint64
	}{{"Run", 3, 20}, {"Action", 7, 30}, {"Sichuan", 4, 40}, {"Hopscotch", 2, 50}, {"Defense", 3, 60}} {
		got, e := s.endScore(tc.family, wire.AppendVarint(nil, tc.field, tc.value), Run{})
		if e != nil || got != tc.value {
			t.Fatalf("%s score=%d err%v", tc.family, got, e)
		}
	}
	if _, e := s.endScore("Hopscotch", wire.AppendVarint(nil, 2, 10001), Run{}); e == nil {
		t.Fatal("captured area accepted beyond100percent")
	}
}
func TestRhythmEndScoreRejectsStaticScoreAndJudgmentOverflow(t *testing.T) {
	s, _ := makeService(t)
	row := wire.AppendVarint(nil, 5, 1)
	row = wire.AppendVarint(row, 8, 1000)
	row = wire.AppendVarint(row, 10, 3)
	s.design.Tables["RhythmGameMusicTable"] = [][]byte{row}
	if _, e := s.endScore("Rhythm", wire.AppendVarint(nil, 4, 1001), Run{Stage: 1}); e == nil {
		t.Fatal("score above staticmax accepted")
	}
	judgment := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 4)
	req := wire.AppendVarint(nil, 4, 500)
	req = wire.AppendBytes(req, 3, judgment)
	if _, e := s.endScore("Rhythm", req, Run{Stage: 1}); e == nil {
		t.Fatal("more judgments than designed notes accepted")
	}
}

func TestEventBattleChallengesPersistAndAwardOnce(t *testing.T) {
	s, e := makeService(t)
	row := wire.AppendVarint(nil, 4, 1)
	row = wire.AppendVarint(row, 5, 1)
	row = wire.AppendVarint(row, 1, 88)
	row = wire.AppendVarint(row, 3, 1)
	row = wire.AppendVarint(row, 9, 4)
	row = wire.AppendVarint(row, 8, 5)
	s.design.Tables["PackEventBattleTable"] = [][]byte{row}
	s.AttachBattleChallenges(gamedata.EventBattleChallenges{88: {{Type: 8, Reward: gamedata.BattleReward{Type: 3, Count: 25}}, {Type: 2, Value1: 8, Reward: gamedata.BattleReward{Type: 3, Count: 25}}}})
	start := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 5, 17), 8, 1), 9, 1), 4, 88)
	if _, err := s.EnterBattle(start, "sid:first"); err != nil {
		t.Fatal(err)
	}
	end := wire.AppendVarint(nil, 2, 1)
	end = wire.AppendVarint(end, 5, 0)
	body, err := s.CompleteBattle(end, "sid:first")
	if err != nil {
		t.Fatal(err)
	}
	info, _, _ := wire.Bytes(body, 16)
	indexes, err := challengeTestIndexes(info)
	if err != nil || len(indexes) != 1 || indexes[0] != 0 || e.granted != 30 {
		t.Fatalf("challenge field16=%x reward%d err%v", info, e.granted, err)
	}
	if _, err = s.EnterBattle(start, "sid:second"); err != nil {
		t.Fatal(err)
	}
	end = wire.AppendVarint(end, 5, 1)
	body, err = s.CompleteBattle(end, "sid:second")
	if err != nil {
		t.Fatal(err)
	}
	info, _, _ = wire.Bytes(body, 16)
	indexes, _ = challengeTestIndexes(info)
	if len(indexes) != 2 || e.granted != 60 {
		t.Fatalf("cumulative indexes %+v grant%d", indexes, e.granted)
	}
	if _, err = s.EnterBattle(start, "sid:third"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteBattle(end, "sid:third"); err != nil {
		t.Fatal(err)
	}
	if e.granted != 65 {
		t.Fatal("challenge reward paid twice")
	}
}
func TestEventBattleChallengeUnknownAndDuplicateRejectedBeforeCost(t *testing.T) {
	s, e := makeService(t)
	row := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 4, 1), 5, 1), 1, 88)
	s.design.Tables["PackEventBattleTable"] = [][]byte{row}
	s.AttachBattleChallenges(gamedata.EventBattleChallenges{88: {{Type: 8, Reward: gamedata.BattleReward{Type: 3, Count: 25}}}})
	start := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 5, 17), 8, 1), 9, 1), 4, 88)
	s.EnterBattle(start, "sid:x")
	bad := wire.AppendVarint(wire.AppendVarint(nil, 2, 1), 5, 1)
	if _, err := s.CompleteBattle(bad, "sid:x"); err == nil {
		t.Fatal("unknownchallenge")
	}
	bad = wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 2, 1), 5, 0), 5, 0)
	if _, err := s.CompleteBattle(bad, "sid:x"); err == nil {
		t.Fatal("duplicatechallenge")
	}
	if e.cost != 0 || e.granted != 0 {
		t.Fatal("badchallenge mutated economic state")
	}
}

func challengeTestIndexes(b []byte) ([]uint64, error) {
	var out []uint64
	err := wire.Walk(b, func(f wire.Field) error {
		if f.Number == 4 {
			v, _, e := wire.Varint(wire.AppendVarint(nil, 4, numFieldTest(f)), 4)
			if e != nil {
				return e
			}
			out = append(out, v)
		}
		return nil
	})
	return out, err
}
func numFieldTest(f wire.Field) uint64 { v, _ := binary.Uvarint(f.Value); return v }
