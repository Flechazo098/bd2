package world

import (
	"bytes"
	"errors"
	"testing"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
)

func TestPackSummaryUsesUnlockedPackAndCollectedCounts(t *testing.T) {
	s := testService()
	s.packSummaryTargets = map[int]bool{21: true, 22: true}
	s.packs = map[int]map[int]gamedata.QuestDesign{21: s.quests, 22: {1: {ID: 1}}}
	s.transitions = map[int]gamedata.PackTransition{21: {PackID: 21, NextPackID: 22}}
	request := wire.AppendVarint(nil, 1, 7)
	code, got, handled, err := s.Handle("/PackSummaryInfoList", request)
	want := wire.AppendBytes(nil, 1, wire.AppendVarint(nil, 1, 21))
	if err != nil || !handled || code != 625 || !bytes.Equal(got, want) {
		t.Fatalf("initial summary code=%d handled=%v body=%x err=%v", code, handled, got, err)
	}
	if err := s.state.ClearQuest(1, 21); err != nil {
		t.Fatal(err)
	}
	_, got, _, err = s.Handle("/PackSummaryInfoList", request)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("quest completion changed collected rewards: %x %v", got, err)
	}
	for _, id := range []int{2, 3} {
		if err := s.state.ClearQuest(id, 21); err != nil {
			t.Fatal(err)
		}
	}
	_, got, _, err = s.Handle("/PackSummaryInfoList", request)
	want = wire.AppendBytes(want, 1, wire.AppendVarint(nil, 1, 22))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("unlocked summary=%x want=%x err=%v", got, want, err)
	}
	// A non-target pack must never enter the reward summary even if it is
	// represented in the account's PackDBInfo rows.
	delete(s.packSummaryTargets, 21)
	_, got, _, err = s.Handle("/PackSummaryInfoList", request)
	want = wire.AppendBytes(nil, 1, wire.AppendVarint(nil, 1, 22))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("filtered summary=%x want=%x err=%v", got, want, err)
	}
}

func TestPackSummaryRejectsInvalidSequence(t *testing.T) {
	s := testService()
	for _, request := range [][]byte{nil, {8}, wire.AppendVarint(nil, 1, 0), wire.AppendVarint(nil, 1, 0x80000000)} {
		_, _, handled, err := s.Handle("/PackSummaryInfoList", request)
		if !handled || !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("request=%x handled=%v err=%v", request, handled, err)
		}
	}
}
