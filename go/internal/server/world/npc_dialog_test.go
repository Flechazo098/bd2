package world

import (
	"bd2server/internal/server/wire"
	"bytes"
	"testing"
)

func TestNpcTaskQuestUpdateRejectsUnavailableAndReplaysClear(t *testing.T) {
	s := testService()
	req := func(quest, pack, value uint64) []byte {
		out := wire.AppendVarint(nil, 1, 1)
		out = wire.AppendVarint(out, 2, quest)
		out = wire.AppendVarint(out, 3, pack)
		return wire.AppendVarint(out, 4, value)
	}
	for _, bad := range [][]byte{req(99, 21, 1), req(2, 21, 1), req(1, 22, 1)} {
		if _, _, _, err := s.handleQuestUpdate(bad); err == nil {
			t.Fatalf("unavailable update accepted: %x", bad)
		}
	}
	code, body, ok, err := s.handleQuestUpdate(req(1, 21, 1))
	expected := wire.AppendBytes(wire.AppendVarint(nil, 1, 1), 2, nil)
	if err != nil || code != 19 || !ok || !bytes.Equal(body, expected) {
		t.Fatalf("update: %d %x %v %v", code, body, ok, err)
	}
	current, found := s.state.QuestInPack(1, 21)
	if !found || len(current.Values) != 1 || current.Values[0] != 1 {
		t.Fatal("task progress not persisted")
	}
	if err = s.state.ClearQuest(1, 21); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.handleQuestUpdate(req(1, 21, 2)); err != nil {
		t.Fatal(err)
	}
	current, found = s.state.QuestInPack(1, 21)
	if !s.state.QuestCleared(1, 21) || !found || len(current.Values) != 1 || current.Values[0] != 1 {
		t.Fatal("cleared quest replay changed committed progress")
	}
}
