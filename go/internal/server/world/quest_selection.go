package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/progress"
	"bd2server/internal/server/wire"
	"encoding/binary"
	"fmt"
)

func (s *Service) questDifficultyFor(packID, questID int) int {
	if s.storyCatalog != nil && s.storyCatalog.Packs[packID].Quests[questID].Type != 0 {
		return 0
	}
	return s.questDifficulty(packID)
}
func (s *Service) questDifficulty(packID int) int {
	selection, _ := s.state.Selection(packID)
	return selection.Difficulty
}
func (s *Service) CurrentQuestDifficulty() (uint64, error) {
	pack, err := s.CurrentPackID()
	if err != nil {
		return 0, err
	}
	return uint64(s.questDifficulty(pack)), nil
}
func questRewardIdentity(packID, questID, difficulty int) string {
	if difficulty == 0 {
		return fmt.Sprintf("pack%d:quest%d", packID, questID)
	}
	return fmt.Sprintf("pack%d:difficulty%d:quest%d", packID, difficulty, questID)
}
func (s *Service) questInfoWire(packID, questID int) []byte {
	out := wire.AppendVarint(nil, 1, uint64(questID))
	selection, _ := s.state.Selection(packID)
	if s.storyCatalog != nil && s.storyCatalog.Packs[packID].Quests[questID].Type != 0 {
		selection = progress.QuestSelection{}
	}
	if current, ok := s.state.QuestInPack(questID, packID, selection.Difficulty); ok {
		for _, value := range current.Values {
			out = wire.AppendVarint(out, 3, uint64(value))
		}
	}
	if selection.Difficulty != 0 {
		out = wire.AppendVarint(out, 4, uint64(selection.Difficulty))
	}
	if selection.Option != 0 {
		out = wire.AppendVarint(out, 5, uint64(selection.Option))
	}
	return wire.AppendVarint(out, 6, uint64(packID))
}
func (s *Service) questLevelInfoWire(packID, difficulty int) []byte {
	out := wire.AppendVarint(nil, 1, uint64(packID))
	out = wire.AppendVarint(out, 2, uint64(difficulty))
	last := 0
	complete := true
	pack := s.storyCatalog.Packs[packID]
	for _, id := range pack.MainQuestIDs {
		if !s.state.QuestCleared(id, packID, difficulty) {
			complete = false
			break
		}
		last = id
	}
	out = wire.AppendVarint(out, 3, uint64(last))
	selection, _ := s.state.Selection(packID)
	if selection.Difficulty == difficulty {
		out = wire.AppendVarint(out, 4, uint64(selection.Option))
	}
	if complete && len(pack.MainQuestIDs) > 0 {
		out = wire.AppendVarint(out, 5, 1)
	}
	return out
}
func (s *Service) handleQuestSelection(path string, request []byte) (int, []byte, bool, error) {
	if path != "/QuestInfo" && s.battleActive != nil && s.battleActive() {
		return 0, nil, true, fmt.Errorf("%w: active battle", ErrInvalidRequest)
	}
	if path == "/QuestInfo" {
		pack, err := requestPack(request)
		if err != nil || !s.packUnlocked(pack) {
			return 0, nil, true, ErrInvalidRequest
		}
		var out []byte
		if active := s.firstUnclearedQuestFor(pack); active != 0 {
			out = wire.AppendBytes(out, 1, s.questInfoWire(pack, active))
		}
		for _, quest := range s.activeSideQuestWires(pack) {
			out = wire.AppendBytes(out, 1, quest)
		}
		var todayCleared []int
		if s.todayQuests != nil {
			rows, ids, err := s.todayQuests.Info(pack)
			if err != nil {
				return 0, nil, true, err
			}
			todayCleared = ids
			for _, row := range rows {
				out = wire.AppendBytes(out, 1, row)
			}
		}
		var cleared []byte
		for _, id := range s.state.ClearedQuests(pack, s.questDifficulty(pack)) {
			cleared = binary.AppendUvarint(cleared, uint64(id))
		}
		for _, id := range todayCleared {
			cleared = binary.AppendUvarint(cleared, uint64(id))
		}
		if len(cleared) > 0 {
			out = wire.AppendBytes(out, 2, cleared)
		}
		return 16, out, true, nil
	}
	quest, pack, err := requestQuest(request)
	if err != nil || !s.packUnlocked(pack) {
		return 0, nil, true, ErrInvalidRequest
	}
	designs, known := s.questsFor(pack)
	design, exists := designs[quest]
	if !known || !exists {
		return 0, nil, true, ErrInvalidRequest
	}
	if path == "/QuestGiveUp" {
		if design.Type != 0 {
			if _, ok := s.state.QuestInPack(quest, pack); !ok {
				return 0, nil, true, ErrInvalidRequest
			}
			if err := s.state.RemoveQuest(quest, pack, 0); err != nil {
				return 0, nil, true, err
			}
			return 20, wire.AppendVarint(nil, 1, uint64(quest)), true, nil
		}
		selection, ok := s.state.Selection(pack)
		if !ok || selection.QuestID != quest || selection.Difficulty == 0 {
			return 0, nil, true, ErrInvalidRequest
		}
		// Abandoning a difficulty retains its independently committed clears.
		if err := s.state.SelectQuest(pack, progress.QuestSelection{}); err != nil {
			return 0, nil, true, err
		}
		return 20, wire.AppendVarint(nil, 1, uint64(quest)), true, nil
	}
	level, _, err := wire.Varint(request, 4)
	if err != nil || level > 4 {
		return 0, nil, true, ErrInvalidRequest
	}
	opt, _, err := wire.Varint(request, 5)
	if err != nil || opt != 0 {
		return 0, nil, true, ErrInvalidRequest
	}
	if level > 0 && !s.questDifficulties[pack][int(level)] {
		return 0, nil, true, ErrInvalidRequest
	}
	if design.Type != 0 {
		if design.Type != 1 {
			return 0, nil, true, ErrInvalidRequest
		}
		if level != 0 || (design.PriorQuestID != 0 && !s.state.QuestCleared(design.PriorQuestID, pack)) || s.state.QuestCleared(quest, pack) {
			return 0, nil, true, ErrInvalidRequest
		}
		if err := s.state.AcceptQuest(quest, pack, 0); err != nil {
			return 0, nil, true, err
		}
		out := wire.AppendBytes(nil, 1, s.questInfoWire(pack, quest))
		items, err := s.ensureQuestItems(pack, quest)
		if err != nil {
			return 0, nil, true, err
		}
		for _, item := range items {
			out = wire.AppendBytes(out, 4, player.ItemWire(item))
		}
		return 17, out, true, nil
	}
	if level > 0 {
		ids := s.storyCatalog.Packs[pack].MainQuestIDs
		for _, id := range ids {
			if !s.state.QuestCleared(id, pack, int(level)-1) {
				return 0, nil, true, fmt.Errorf("%w: previous difficulty is incomplete", ErrInvalidRequest)
			}
		}
	}
	// Accept only the first unfinished main quest in this difficulty, using
	// authoritative links rather than permitting an arbitrary reward jump.
	first := 0
	for _, id := range s.storyCatalog.Packs[pack].MainQuestIDs {
		if !s.state.QuestCleared(id, pack, int(level)) {
			first = id
			break
		}
	}
	if quest != first || first == 0 {
		return 0, nil, true, ErrInvalidRequest
	}
	if err := s.state.SelectQuest(pack, progress.QuestSelection{QuestID: quest, Difficulty: int(level), Option: int(opt)}); err != nil {
		return 0, nil, true, err
	}
	if err := s.state.SetActivePackID(pack); err != nil {
		return 0, nil, true, err
	}
	s.setCurrentPack(pack)
	chars, decks, err := s.resolveActivePartyWires(pack, quest)
	if err != nil {
		return 0, nil, true, err
	}
	out := wire.AppendBytes(nil, 1, s.questInfoWire(pack, quest))
	for _, char := range chars {
		out = wire.AppendBytes(out, 2, char)
	}
	for _, deck := range decks {
		out = wire.AppendBytes(out, 3, deck)
	}
	items, err := s.ensureQuestItems(pack, quest)
	if err != nil {
		return 0, nil, true, err
	}
	for _, item := range items {
		out = wire.AppendBytes(out, 4, player.ItemWire(item))
	}
	return 17, out, true, nil
}

func (s *Service) activeSideQuestWires(packID int) [][]byte {
	var out [][]byte
	if s.storyCatalog == nil {
		return nil
	}
	for _, quest := range s.state.QuestsInPack(packID, 0) {
		if s.storyCatalog.Packs[packID].Quests[quest.QuestID].Type == 1 && !s.state.QuestCleared(quest.QuestID, packID) {
			out = append(out, s.questInfoWire(packID, quest.QuestID))
		}
	}
	return out
}

func (s *Service) visibleOwnedCharacters(characters []player.Character) []player.Character {
	earned := s.state.QuestCleared(s.seed.BattleUnlockQuestID, s.seed.PackID)
	out := make([]player.Character, 0, len(characters))
	for _, character := range characters {
		if !player.IsStoryCharacter(character) && (earned || s.seed.RewardCharacter.InvenIndex == 0 || character.InvenIndex != s.seed.RewardCharacter.InvenIndex) {
			out = append(out, character)
		}
	}
	return out
}

func (s *Service) ensureQuestItems(packID, questID int) ([]player.Item, error) {
	ids := s.storyCatalog.Packs[packID].Quests[questID].GiveQuestItemIDs
	if len(ids) == 0 {
		return nil, nil
	}
	if s.inventory == nil {
		return nil, fmt.Errorf("world: quest item inventory unavailable")
	}
	var rewards []gamedata.BattleReward
	for _, id := range ids {
		rewards = append(rewards, gamedata.BattleReward{Type: 13, ID: id, Count: 1})
	}
	identity := questRewardIdentity(packID, questID, s.questDifficultyFor(packID, questID)) + ":give-items"
	items, err := s.inventory.GrantOnce(identity, rewards)
	if err == nil && len(items) == 0 {
		items = s.inventory.GrantedItems(identity)
	}
	return items, err
}

func (s *Service) AttachBattleActive(provider func() bool) error {
	if provider == nil {
		return fmt.Errorf("world: nil battle activity provider")
	}
	s.battleActive = provider
	return nil
}
