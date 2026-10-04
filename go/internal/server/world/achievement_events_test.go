package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"testing"
)

func TestAchievementEventsMatchDesignAndReplayAcrossReopen(t *testing.T) {
	store := stateio.NewMemory()
	design := &gamedata.AchievementCounterDesign{Groups: map[int][]int{987: {0, 1}, 654: {0}}, Conditions: map[int]gamedata.AchievementCondition{987: {Type: 54}, 654: {Type: 54, SubType: 2}}}
	s, err := NewAchievementService(design, store)
	if err != nil {
		t.Fatal(err)
	}
	updates, err := s.RecordEvent("draw:1", 54, 0, 10)
	if err != nil || len(updates) != 1 {
		t.Fatalf("updates=%d err=%v", len(updates), err)
	}
	value, _, _ := wire.Varint(updates[0], 2)
	isSet, _, _ := wire.Varint(updates[0], 3)
	if value != 10 || isSet != 1 {
		t.Fatal("notify not absolute")
	}
	s, err = NewAchievementService(design, store)
	if err != nil {
		t.Fatal(err)
	}
	if updates, err := s.RecordEvent("draw:1", 54, 0, 10); err != nil || len(updates) != 0 {
		t.Fatalf("replay=%v err=%v", updates, err)
	}
	if _, err := s.RecordEvent("draw:1", 54, 0, 9); err == nil {
		t.Fatal("replay mismatch accepted")
	}
	if v, err := s.AchievementValue(987); err != nil || v != 10 {
		t.Fatalf("value=%d err=%v", v, err)
	}
	if v, _ := s.AchievementValue(654); v != 0 {
		t.Fatal("wrong subtype incremented")
	}
	if updates, err := s.SetCondition(54, 2, 7); err != nil || len(updates) != 1 {
		t.Fatal(err)
	}
	if updates, err := s.SetCondition(54, 2, 7); err != nil || len(updates) != 0 {
		t.Fatal("absolute unchanged notified")
	}
}
