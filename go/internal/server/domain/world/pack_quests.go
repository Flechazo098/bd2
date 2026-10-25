package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"fmt"
)

func (s *Service) packQuestDesigns(packID int) map[int]gamedata.QuestDesign {
	if s.storyCatalog != nil {
		if pack, found := s.storyCatalog.Packs[packID]; found {
			return pack.Quests
		}
	}
	if pack := s.fieldPacks[packID]; len(pack.Quests) != 0 {
		return pack.Quests
	}
	return nil
}

func (s *Service) mainQuestIDs(packID int) []int {
	if s.storyCatalog != nil {
		if pack, found := s.storyCatalog.Packs[packID]; found {
			return pack.MainQuestIDs
		}
	}
	return s.fieldPacks[packID].MainQuestIDs
}

func additionalQuestCurrency(reward gamedata.Reward) bool {
	_, exists := events.AdditionalCurrencyFields[reward.Type]
	return exists
}

func (s *Service) grantAdditionalQuestCurrencies(ctx command.Context, packID, quest int, rewards []gamedata.Reward) ([]byte, error) {
	var currencies []gamedata.Reward
	for _, reward := range rewards {
		if additionalQuestCurrency(reward) {
			currencies = append(currencies, reward)
		}
	}
	if len(currencies) == 0 {
		return nil, nil
	}
	if s.fieldPackEconomy == nil {
		return nil, fmt.Errorf("world: quest currency economy unavailable")
	}
	return s.fieldPackEconomy.Apply(ctx, questRewardIdentity(packID, quest, s.questDifficultyFor(packID, quest))+":currencies", nil, currencies)
}
