package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
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
func (s *Service) CurrentQuestDifficulty(ctx command.Context) (uint64, error) {
	pack, err := s.CurrentPackID(ctx)
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

func (s *Service) visibleOwnedCharacters(characters []roster.Character) []roster.Character {
	earned := s.state.QuestCleared(s.seed.BattleUnlockQuestID, s.seed.PackID)
	out := make([]roster.Character, 0, len(characters))
	for _, character := range characters {
		if !roster.IsStoryCharacter(character) && (earned || s.seed.RewardCharacter.InvenIndex == 0 || character.InvenIndex != s.seed.RewardCharacter.InvenIndex) {
			out = append(out, character)
		}
	}
	return out
}

func (s *Service) ensureQuestItems(ctx command.Context, packID, questID int) ([]assets.Item, error) {
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
	items, err := s.inventory.GrantOnce(ctx, identity, rewards)
	if err == nil && len(items) == 0 {
		items = s.inventory.GrantedItems(identity)
	}
	return items, err
}

func (s *Service) AttachBattleActive(provider func(command.Context) bool) error {
	if provider == nil {
		return fmt.Errorf("world: nil battle activity provider")
	}
	s.battleActive = provider
	return nil
}
