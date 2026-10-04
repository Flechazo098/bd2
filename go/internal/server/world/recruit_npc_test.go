package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"strconv"
	"testing"
)

func recruitNPCPosition(t *testing.T, s *Service, pack, mapID int) {
	t.Helper()
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, uint64(pack))
	request = wire.AppendString(request, 3, `{"MapId":`+strconv.Itoa(mapID)+`,"PlayerPosition":{"x":0,"y":0,"z":0},"ColleaguePositions":[]}`)
	if err := s.state.SaveUserPosition(request); err != nil {
		t.Fatal(err)
	}
}

func TestResolveRecruitNPCRequiresMapStoryCompletionAndOrdinaryType(t *testing.T) {
	s := testService()
	d := &gamedata.RecruitDesign{Rules: map[uint64]gamedata.RecruitRule{40: {ID: 40, Type: 0, PackID: 21}}}
	npc := gamedata.RecruitNPC{ID: 7, MapID: 9, ScoutID: 40}
	if _, err := s.resolveRecruitNPC(npc, d); err == nil {
		t.Fatal("accepted NPC without saved map")
	}
	recruitNPCPosition(t, s, 21, 9)
	if _, err := s.resolveRecruitNPC(npc, d); err == nil {
		t.Fatal("accepted incomplete story")
	}
	for _, id := range []int{1, 2, 3} {
		if err := s.state.ClearQuest(id, 21); err != nil {
			t.Fatal(err)
		}
	}
	if id, err := s.resolveRecruitNPC(npc, d); err != nil || id != 40 {
		t.Fatalf("resolved=%d err=%v", id, err)
	}
	recruitNPCPosition(t, s, 21, 8)
	if _, err := s.resolveRecruitNPC(npc, d); err == nil {
		t.Fatal("accepted NPC in other map")
	}
	recruitNPCPosition(t, s, 21, 9)
	d.Rules[40] = gamedata.RecruitRule{ID: 40, Type: 1, PackID: 21}
	if _, err := s.resolveRecruitNPC(npc, d); err == nil {
		t.Fatal("accepted special NPC through ordinary endpoint")
	}
}

func TestResolveRecruitNPCBlocksActiveQuestRangesAfterEarlierRecruitChapter(t *testing.T) {
	s := testService()
	s.activePack = 22
	s.packs = map[int]map[int]gamedata.QuestDesign{21: s.quests, 22: {1: {ID: 1}, 2: {ID: 2}}}
	s.transitions = map[int]gamedata.PackTransition{21: {PackID: 21, NextPackID: 22}}
	for _, id := range []int{1, 2, 3} {
		if err := s.state.ClearQuest(id, 21); err != nil {
			t.Fatal(err)
		}
	}
	recruitNPCPosition(t, s, 22, 9)
	d := &gamedata.RecruitDesign{Rules: map[uint64]gamedata.RecruitRule{40: {ID: 40, Type: 0, PackID: 21}}}
	npc := gamedata.RecruitNPC{ID: 7, MapID: 9, ScoutID: 40, QuestEnableTypes: []uint64{0}, QuestRanges: []uint64{1}, QuestTypes: map[uint64]uint64{1: 0, 2: 0}}
	if _, err := s.resolveRecruitNPC(npc, d); err == nil {
		t.Fatal("accepted NPC used by active main quest")
	}
	npc.QuestRanges[0] = 2
	if id, err := s.resolveRecruitNPC(npc, d); err != nil || id != 40 {
		t.Fatalf("future quest blocked NPC=%d err=%v", id, err)
	}
	npc.QuestTypes[1] = 1
	npc.QuestRanges[0] = 1
	request := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 1), 3, 22)
	if _, err := s.state.UpdateQuest(request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.resolveRecruitNPC(npc, d); err == nil {
		t.Fatal("accepted NPC used by active subquest")
	}
	if err := s.state.ClearQuest(1, 22); err != nil {
		t.Fatal(err)
	}
	if id, err := s.resolveRecruitNPC(npc, d); err != nil || id != 40 {
		t.Fatalf("cleared subquest still blocked NPC=%d err=%v", id, err)
	}
}
