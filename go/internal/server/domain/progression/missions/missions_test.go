package missions

import (
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/progression/conditions"
	"bd2server/internal/server/storage/stateio"
)

type recordingHandler struct{ events []conditions.Event }

func (h *recordingHandler) Handle(command.Context, string, []byte) (int, []byte, bool, error) {
	return 0, nil, false, nil
}

func (h *recordingHandler) RecordEvent(_ command.Context, event conditions.Event, _ func(command.Context, uint64, uint64) bool) error {
	h.events = append(h.events, event)
	return nil
}

func newTestService(t *testing.T, design *gamedata.MissionDesign) (command.Context, *Service) {
	t.Helper()
	ctx := command.Context{State: stateio.NewMemory()}
	inventory, err := assets.OpenInventory(ctx, stateio.RootStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(ctx, stateio.RootStore{}, design, inventory)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, service
}

// Completed regular missions must reach scheduled event missions as same-group
// and other-group completion events, while a completion in another group does
// not advance the other-group counter.
func TestCompletionsForwardGroupEvents(t *testing.T) {
	daily := gamedata.MissionKey{GroupType: 0, GroupID: 1, ID: 1}
	weekly := gamedata.MissionKey{GroupType: 1, GroupID: 2, ID: 2}
	victory := gamedata.MissionKey{GroupType: 1, GroupID: 2, ID: 4}
	design := &gamedata.MissionDesign{
		Missions: map[gamedata.MissionKey][]gamedata.Reward{daily: nil, weekly: nil, victory: nil},
		Conditions: map[gamedata.MissionKey]gamedata.MissionCondition{
			daily:   {Type: conditions.MonsterKill, TargetValue: 1},
			weekly:  {Type: conditions.OtherGroupMissionComplete, SubType: 1, TargetValue: 5},
			victory: {Type: conditions.HuntingBattle, TargetValue: 1},
		},
	}
	ctx, service := newTestService(t, design)
	handler := &recordingHandler{}
	if err := service.AttachEventHandler(ctx, handler); err != nil {
		t.Fatal(err)
	}
	assertEvents := func(want ...conditions.Event) {
		t.Helper()
		if len(handler.events) != len(want) {
			t.Fatalf("events = %+v, want %+v", handler.events, want)
		}
		for i := range want {
			if handler.events[i] != want[i] {
				t.Fatalf("event %d = %+v, want %+v", i, handler.events[i], want[i])
			}
		}
	}
	if err := service.RecordEvent(ctx, conditions.Event{Type: conditions.MonsterKill, Count: 1}, nil); err != nil {
		t.Fatal(err)
	}
	assertEvents(
		conditions.Event{Type: conditions.MonsterKill, Count: 1},
		conditions.Event{Type: conditions.SameGroupMissionComplete, Sub: 1, Count: 1},
		conditions.Event{Type: conditions.OtherGroupMissionComplete, Sub: 1, Count: 1},
	)
	if got := service.state.Progress["1/2/2"]; got != 1 {
		t.Fatalf("other-group counter = %d, want 1", got)
	}
	handler.events = nil
	if err := service.RecordEvent(ctx, conditions.Event{Type: conditions.HuntingBattle, Count: 1}, nil); err != nil {
		t.Fatal(err)
	}
	assertEvents(
		conditions.Event{Type: conditions.HuntingBattle, Count: 1},
		conditions.Event{Type: conditions.SameGroupMissionComplete, Sub: 2, Count: 1},
		conditions.Event{Type: conditions.OtherGroupMissionComplete, Sub: 2, Count: 1},
	)
	if got := service.state.Progress["1/2/2"]; got != 1 {
		t.Fatalf("group 2 completion advanced the group 1 counter to %d", got)
	}
}

// CompletedMissionCount counts only the requested group's reached missions.
func TestCompletedMissionCountFiltersByGroup(t *testing.T) {
	first := gamedata.MissionKey{GroupType: 0, GroupID: 1, ID: 1}
	second := gamedata.MissionKey{GroupType: 0, GroupID: 1, ID: 2}
	other := gamedata.MissionKey{GroupType: 0, GroupID: 2, ID: 3}
	design := &gamedata.MissionDesign{
		Missions: map[gamedata.MissionKey][]gamedata.Reward{first: nil, second: nil, other: nil},
		Conditions: map[gamedata.MissionKey]gamedata.MissionCondition{
			first:  {Type: conditions.MonsterKill, TargetValue: 1},
			second: {Type: conditions.HuntingBattle, TargetValue: 1},
			other:  {Type: conditions.GachaBuy, TargetValue: 1},
		},
	}
	ctx, service := newTestService(t, design)
	if err := service.RecordEvent(ctx, conditions.Event{Type: conditions.MonsterKill, Count: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if got := service.CompletedMissionCount(1); got != 1 {
		t.Fatalf("group 1 count after one mission = %d, want 1", got)
	}
	if err := service.RecordEvent(ctx, conditions.Event{Type: conditions.HuntingBattle, Count: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordEvent(ctx, conditions.Event{Type: conditions.GachaBuy, Count: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if got := service.CompletedMissionCount(1); got != 2 {
		t.Fatalf("group 1 count = %d, want 2", got)
	}
	if got := service.CompletedMissionCount(2); got != 1 {
		t.Fatalf("group 2 count = %d, want 1", got)
	}
	if got := service.CompletedMissionCount(3); got != 0 {
		t.Fatalf("unknown group count = %d, want 0", got)
	}
}
