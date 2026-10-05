package eventtasks

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"testing"
	"time"
)

type economyStub struct {
	calls   int
	rewards []gamedata.Reward
}

func (e *economyStub) Apply(_ string, _ []gamedata.Reward, r []gamedata.Reward) ([]byte, error) {
	e.calls++
	e.rewards = append(e.rewards, r...)
	return []byte{10, 0}, nil
}
func setup(t *testing.T) (*Service, *economyStub, stateio.Store) {
	t.Helper()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	registry := events.NewRegistry()
	if err := registry.Replace([]events.Schedule{{UID: 1, Type: 0, ID: 1, Start: now.Add(-24 * time.Hour).UnixMilli(), End: now.Add(24 * time.Hour).UnixMilli()}, {UID: 2, Type: 4, ID: 7, Start: now.Add(-24 * time.Hour).UnixMilli(), End: now.Add(24 * time.Hour).UnixMilli()}, {UID: 3, Type: 5, ID: 8, Start: now.Add(-24 * time.Hour).UnixMilli(), End: now.Add(24 * time.Hour).UnixMilli()}}); err != nil {
		t.Fatal(err)
	}
	d := &gamedata.EventTasksDesign{Attendance: map[uint64]gamedata.EventAttendance{1: {ID: 1, Group: 1}}, AttendanceRewards: map[uint64][]gamedata.EventAttendanceReward{1: {{ID: 1, Group: 1, Day: 1, Basic: gamedata.Reward{Type: 4, Count: 100}}}}, MissionGroups: map[uint64]gamedata.EventMissionGroup{7: {ID: 7, Groups: []uint64{9}}}, Missions: map[uint64]gamedata.EventTask{10: {ID: 10, Group: 9, Type: 2, Target: 2, PassExp: 20, Rewards: []gamedata.Reward{{Type: 4, Count: 5}}}}, Passes: map[uint64]gamedata.EventPass{8: {ID: 8, MissionGroup: 7, LevelGroup: 8}}, PassLevels: map[uint64][]gamedata.EventPassLevel{8: {{ID: 1, NeedExp: 20, Basic: gamedata.Reward{Type: 8, ID: 1000, Count: 1}}}}}
	e := &economyStub{}
	store := stateio.NewMemory()
	s, err := Open(store, d, registry, e)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	s.SetSession("test")
	return s, e, store
}
func req(seq uint64) []byte { return wire.AppendVarint(nil, 1, seq) }
func TestMissionRejectsFabricatedProgressAndReplaySurvivesRestart(t *testing.T) {
	s, e, store := setup(t)
	update := req(1)
	x := wire.AppendVarint(nil, 1, 9)
	x = wire.AppendVarint(x, 2, 10)
	x = wire.AppendVarint(x, 3, 2)
	x = wire.AppendVarint(x, 4, 7)
	update = wire.AppendBytes(update, 2, x)
	if _, _, _, err := s.Handle("/MissionUpdate", update); err == nil {
		t.Fatal("client manufactured completion")
	}
	if err := s.RecordEvent(2, 0, 2, nil); err != nil {
		t.Fatal(err)
	}
	clear := req(2)
	clear = wire.AppendVarint(clear, 2, 1)
	clear = wire.AppendVarint(clear, 3, 2)
	clear = wire.AppendVarint(clear, 4, 9)
	clear = wire.AppendVarint(clear, 6, 7)
	_, reply, _, err := s.Handle("/MissionClear", clear)
	if err != nil {
		t.Fatal(err)
	}
	if e.calls != 1 || s.pass(s.registry.List()[2]).Exp != 20 {
		t.Fatal("settlement or pass exp missing")
	}
	next, err := Open(store, s.design, s.registry, e)
	if err != nil {
		t.Fatal(err)
	}
	next.now = s.now
	next.SetSession("test")
	_, replay, _, err := next.Handle("/MissionClear", clear)
	if err != nil || !bytes.Equal(reply, replay) || e.calls != 1 {
		t.Fatal("restart replay duplicated reward")
	}
	mutated := wire.AppendVarint(clear, 5, 10)
	if _, _, _, err = next.Handle("/MissionClear", mutated); err == nil {
		t.Fatal("changed replay accepted")
	}
	passReq := wire.AppendVarint(req(3), 2, 1)
	passReq = wire.AppendVarint(passReq, 3, 8)
	if _, _, _, err = next.Handle("/PassReward", passReq); err != nil {
		t.Fatal(err)
	}
	if e.calls != 2 {
		t.Fatal("pass reward not granted")
	}
}
func TestAttendanceDailyCounterAndClaimEligibility(t *testing.T) {
	s, e, _ := setup(t)
	if _, _, _, err := s.Handle("/Attendance", req(1)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Handle("/Attendance", req(2)); err != nil {
		t.Fatal(err)
	}
	if s.state.Attendance["1"].Count != 1 {
		t.Fatal("reconnect counted attendance twice")
	}
	claim := req(3)
	claim = wire.AppendVarint(claim, 2, 1)
	claim = wire.AppendVarint(claim, 3, 1)
	claim = wire.AppendVarint(claim, 4, 1)
	if _, _, _, err := s.Handle("/EventReward", claim); err != nil {
		t.Fatal(err)
	}
	if e.calls != 1 {
		t.Fatal("attendance reward missing")
	}
	again := wire.AppendVarint(nil, 1, 4)
	again = append(again, claim[2:]...)
	if _, _, _, err := s.Handle("/EventReward", again); err == nil {
		t.Fatal("claimed attendance awarded again")
	}
}

func TestAttendanceNextGroupAndRepeatedChainReward(t *testing.T) {
	s, e, _ := setup(t)
	s.design.Attendance[1] = gamedata.EventAttendance{ID: 1, Group: 20}
	s.design.AttendanceGroups = map[[2]uint64]gamedata.EventAttendanceGroup{{20, 5}: {Group: 20, ID: 5, Next: 6}, {20, 6}: {Group: 20, ID: 6, Next: 5}}
	s.design.AttendanceRewards = map[uint64][]gamedata.EventAttendanceReward{5: {{Group: 5, ID: 1, Day: 1, Basic: gamedata.Reward{Type: 4, Count: 5}}}, 6: {{Group: 6, ID: 1, Day: 1, Basic: gamedata.Reward{Type: 4, Count: 6}}}}
	today := s.now()
	s.now = func() time.Time { return today }
	registry := events.NewRegistry()
	if err := registry.Replace([]events.Schedule{{UID: 1, Type: 0, ID: 1, Start: today.Add(-24 * time.Hour).UnixMilli(), End: today.Add(7 * 24 * time.Hour).UnixMilli()}}); err != nil {
		t.Fatal(err)
	}
	s.registry = registry
	seq := uint64(1)
	for _, group := range []uint64{5, 6, 5} {
		if _, _, _, err := s.Handle("/Attendance", req(seq)); err != nil {
			t.Fatal(err)
		}
		seq++
		if s.state.Attendance["1"].Group != group || s.state.Attendance["1"].Count != 1 {
			t.Fatalf("wrong chain state %+v", s.state.Attendance["1"])
		}
		claim := wire.AppendVarint(req(seq), 2, 1)
		claim = wire.AppendVarint(claim, 3, group)
		claim = wire.AppendVarint(claim, 4, 1)
		if _, _, _, err := s.Handle("/EventReward", claim); err != nil {
			t.Fatal(err)
		}
		seq++
		today = today.Add(24 * time.Hour)
	}
	if e.calls != 3 || len(s.state.Attendance["1"].History) != 2 {
		t.Fatal("repeated cycle reward or retained history missing")
	}
}

func TestPrivateServerPremiumCashPassOnceAndRestartReplay(t *testing.T) {
	s, e, store := setup(t)
	s.design.PassBuys = map[uint64][]gamedata.EventPassBuy{8: {{ID: 1, Type: 1, CashID: 42, Rewards: []gamedata.Reward{{Type: 4, Count: 10}}}}}
	request := wire.AppendVarint(req(1), 2, 8)
	request = wire.AppendVarint(request, 3, 1)
	_, reply, _, err := s.Handle("/PassBuy", request)
	if err != nil {
		t.Fatal(err)
	}
	if e.calls != 1 || !s.state.Passes["3"].Premium {
		t.Fatal("premium cash pass not activated")
	}
	reopened, err := Open(store, s.design, s.registry, e)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = s.now
	reopened.SetSession("test")
	_, again, _, err := reopened.Handle("/PassBuy", request)
	if err != nil || !bytes.Equal(reply, again) || e.calls != 1 {
		t.Fatal("cash pass replay duplicated reward")
	}
	duplicate := wire.AppendVarint(req(2), 2, 8)
	duplicate = wire.AppendVarint(duplicate, 3, 1)
	if _, _, _, err = reopened.Handle("/PassBuy", duplicate); err == nil || e.calls != 1 {
		t.Fatal("premium cash purchase granted twice")
	}
}
