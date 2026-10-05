package eventtasks

import (
	"bd2server/internal/server/gamedata"
	"testing"
)

// MC_FIELD_SPAWN_EVENT_REWARD subtype zero is the normal monster kind,
// unlike other event conditions where a zero subtype means any item.
func TestTrackerMissionsSeparateNormalAndSpecialRewards(t *testing.T) {
	s, _, _ := setup(t)
	s.design.Missions = map[uint64]gamedata.EventTask{10: {ID: 10, Group: 9, Type: 349, SubType: 0, Target: 20}, 11: {ID: 11, Group: 9, Type: 349, SubType: 1, Target: 5}}
	if err := s.RecordEvent(349, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	v, err := s.registry.Resolve(2)
	if err != nil {
		t.Fatal(err)
	}
	if s.mission(v, 10).Value != 0 || s.mission(v, 11).Value != 1 {
		t.Fatal("special capture advanced normal capture mission")
	}
	if err := s.RecordEvent(349, 0, 1, nil); err != nil {
		t.Fatal(err)
	}
	if s.mission(v, 10).Value != 1 || s.mission(v, 11).Value != 1 {
		t.Fatal("normal capture advanced special capture mission")
	}
}
