package world

import (
	"bd2server/internal/server/gamedata"
	"fmt"
)

func (s *Service) ResolveRecruitNPC(npcID uint64, root, version string, design *gamedata.RecruitDesign) (uint64, error) {
	pack, err := s.CurrentPackID()
	if err != nil {
		return 0, err
	}
	npc, err := gamedata.LoadRecruitNPC(root, version, pack, npcID)
	if err != nil {
		return 0, err
	}
	return s.resolveRecruitNPC(npc, design)
}

func (s *Service) resolveRecruitNPC(npc gamedata.RecruitNPC, design *gamedata.RecruitDesign) (uint64, error) {
	if design == nil || s.state == nil {
		return 0, fmt.Errorf("world: recruitment unavailable")
	}
	pack, err := s.CurrentPackID()
	if err != nil {
		return 0, err
	}
	position, found := s.state.Position()
	if !found || position.PackID != pack || position.Position.MapID <= 0 || uint64(position.Position.MapID) != npc.MapID {
		return 0, fmt.Errorf("world: recruit NPC outside saved current map")
	}
	rule, found := design.Rules[npc.ScoutID]
	if !found || rule.Type != 0 || rule.PackID == 0 || !s.packCompleteFor(int(rule.PackID)) {
		return 0, fmt.Errorf("world: ordinary recruitment requires completed story pack")
	}
	// QuestPacket treats main/sub quest NPC use separately. The local server
	// has one story difficulty and tracks active quest progress by pack; reject
	// a referenced active quest using the client's == / <= range branches.
	for i, quest := range npc.QuestRanges {
		kind, known := npc.QuestTypes[quest]
		if quest == 0 || !known || kind > 1 {
			continue
		}
		if kind == 0 && s.packCompleteFor(pack) {
			continue
		}
		quests, knownPack := s.questsFor(pack)
		if !knownPack {
			return 0, fmt.Errorf("world: recruit NPC quest pack unavailable")
		}
		for id := range quests {
			activeKind, known := npc.QuestTypes[uint64(id)]
			if !known || activeKind != kind {
				continue
			}
			if s.state.QuestCleared(id, pack) {
				continue
			}
			active := false
			if kind == 0 {
				active = s.canClear(pack, id)
			} else {
				_, active = s.state.QuestInPack(id, pack)
			}
			if !active {
				continue
			}
			match := uint64(id) == quest
			if npc.QuestEnableTypes[i] == 1 || npc.QuestEnableTypes[i] == 3 {
				match = quest <= uint64(id)
			}
			if match {
				return 0, fmt.Errorf("world: recruit NPC is used by active quest")
			}
		}
	}
	return npc.ScoutID, nil
}
