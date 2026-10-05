package eventtasks

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"bd2server/internal/server/world"
	"bytes"
	"encoding/json"
	"testing"
)

func notice(t *testing.T, s *Service, seq uint64) []byte {
	t.Helper()
	b, err := s.AfterDispatch("/MiniGameRouletteInfo", req(seq), nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRouletteReadsDoNotReplayHistoricalMissionNotifications(t *testing.T) {
	s, _, _ := setup(t)
	if err := s.RecordEvent(2, 0, 1, nil); err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{"test", "new-session"} {
		s.BeginSession(session)
		for seq := uint64(1); seq <= 3; seq++ {
			if err := s.BeforeDispatch("/MiniGameRouletteInfo", req(seq)); err != nil {
				t.Fatal(err)
			}
			if got := notice(t, s, seq); len(got) != 0 {
				t.Fatalf("read notified old mission: %x", got)
			}
		}
	}
}

func TestMissionNotificationContainsOnlyChangesAndAbsoluteZero(t *testing.T) {
	s, _, _ := setup(t)
	before, _ := json.Marshal(s.state)
	if err := s.BeforeDispatch("/MiniGameRouletteInfo", req(1)); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(s.state)
	if !bytes.Equal(before, after) {
		t.Fatal("baseline initialized persistent missions")
	}
	if err := s.RecordEvent(2, 0, 1, nil); err != nil {
		t.Fatal(err)
	}
	first := notice(t, s, 1)
	if len(first) == 0 {
		t.Fatal("actual progress did not notify")
	}
	if len(notice(t, s, 1)) != 0 {
		t.Fatal("same request notified twice")
	}
	if err := s.BeforeDispatch("/MiniGameRouletteInfo", req(2)); err != nil {
		t.Fatal(err)
	}
	if len(notice(t, s, 2)) != 0 {
		t.Fatal("subsequent read notified")
	}
	if err := s.BeforeDispatch("/reset", req(3)); err != nil {
		t.Fatal(err)
	}
	for _, m := range s.state.Missions {
		m.Value = 0
	}
	zero := notice(t, s, 3)
	if len(zero) == 0 {
		t.Fatal("zero transition was omitted")
	}
	var value uint64 = 99
	_ = wire.Walk(zero, func(event wire.Field) error {
		groupMap, _, _ := wire.Bytes(event.Value, 2)
		return wire.Walk(groupMap, func(group wire.Field) error {
			rows, _, _ := wire.Bytes(group.Value, 2)
			return wire.Walk(rows, func(row wire.Field) error { value = scalar(row.Value, 2); return nil })
		})
	})
	if value != 0 {
		t.Fatalf("notification is not absolute zero: %d", value)
	}
}

type noticeProvider struct{ count uint64 }

func (p *noticeProvider) Snapshot() (world.GameplayAchievementSnapshot, error) {
	return world.GameplayAchievementSnapshot{Items: map[[2]uint64]uint64{{5, 8}: p.count}}, nil
}
func (*noticeProvider) Events(string, []byte, []byte, world.GameplayAchievementSnapshot, world.GameplayAchievementSnapshot) ([]world.GameplayAchievementEvent, error) {
	return nil, nil
}

func TestObserverBatchRollbackRetryKeepsMissionDelta(t *testing.T) {
	s, _, store := setup(t)
	task := s.design.Missions[10]
	task.Type = 32
	s.design.Missions[10] = task
	p := &noticeProvider{count: 1}
	s.AttachGameplayProvider(p)
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := store.Load("eventtasks")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeforeDispatch("/batch", req(1)); err != nil {
		t.Fatal(err)
	}
	p.count = 2
	first := notice(t, s, 1)
	if len(first) == 0 {
		t.Fatal("observer item gain did not notify")
	}
	// Account transaction rollback restores the domain snapshot and inventory.
	if err = store.Save("eventtasks", checkpoint); err != nil {
		t.Fatal(err)
	}
	s.state = snapshot{}
	if err = json.Unmarshal(checkpoint, &s.state); err != nil {
		t.Fatal(err)
	}
	p.count = 1
	if err = s.BeforeDispatch("/batch", req(1)); err != nil {
		t.Fatal(err)
	}
	p.count = 2
	retry := notice(t, s, 1)
	if !bytes.Equal(first, retry) {
		t.Fatalf("rolled-back retry lost delta: %x / %x", first, retry)
	}
	if err = s.BeforeDispatch("/batch", req(1)); err != nil {
		t.Fatal(err)
	}
	if len(notice(t, s, 1)) != 0 {
		t.Fatal("committed retry repeated delta")
	}
}

func TestMissionDeltaSeparatesPermanentGroupsAndOmitsEmptyGroups(t *testing.T) {
	s, _, _ := setup(t)
	r := events.NewRegistry()
	if err := r.Replace([]events.Schedule{{UID: 0, Type: 4, ID: 7, Start: 1, End: 9999999999999}, {UID: 0, Type: 4, ID: 8, Start: 1, End: 9999999999999}}); err != nil {
		t.Fatal(err)
	}
	s.registry = r
	s.design.MissionGroups[8] = gamedata.EventMissionGroup{ID: 8, Groups: []uint64{11}}
	s.design.Missions[12] = gamedata.EventTask{ID: 12, Group: 11, Type: 99, Target: 2}
	if err := s.RecordEvent(99, 0, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.BeforeDispatch("/play", req(1)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordEvent(2, 0, 1, nil); err != nil {
		t.Fatal(err)
	}
	got := notice(t, s, 1)
	count := 0
	if err := wire.Walk(got, func(f wire.Field) error {
		count++
		if f.Number != 4 || scalar(f.Value, 1) != 7 {
			t.Fatalf("unchanged group emitted: %x", f.Value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("wanted changed group only, got %d", count)
	}
	if err := s.BeforeDispatch("/read", req(2)); err != nil {
		t.Fatal(err)
	}
	if len(notice(t, s, 2)) != 0 {
		t.Fatal("empty event/group emitted")
	}
}
