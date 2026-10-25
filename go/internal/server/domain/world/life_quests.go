package world

import (
	"fmt"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
)

// Life packs carry one quest chain. Its current quest is always active: the
// farm HUD lists it, LifeQuestController reports progress with QuestUpdate,
// and QuestClear accepts the next quest like any non-main quest. The farm's
// first tutorial focuses that HUD entry, so without an active quest it can
// never reach the marker that records its completion.

// ensureLifeQuest accepts the first quest of the chain that is not cleared.
func (s *Service) ensureLifeQuest(ctx command.Context, packID int) error {
	for _, questID := range s.fieldPacks[packID].LifeQuestIDs {
		if s.state.QuestCleared(questID, packID, 0) {
			continue
		}
		if _, active := s.state.QuestInPack(questID, packID, 0); active {
			return nil
		}
		return s.state.AcceptQuest(ctx, questID, packID, 0)
	}
	return nil
}

func lifeReward(reward gamedata.Reward) bool { return reward.Type >= 64 && reward.Type <= 66 }

// grantLifeQuestRewards pays life coins and life items through the life
// economy, which keeps life items out of the ordinary inventory.
func (s *Service) grantLifeQuestRewards(ctx command.Context, packID, quest int, rewards []gamedata.Reward) ([]byte, error) {
	var life []gamedata.Reward
	for _, reward := range rewards {
		if lifeReward(reward) {
			life = append(life, reward)
		}
	}
	if len(life) == 0 {
		return nil, nil
	}
	if s.fieldPackEconomy == nil {
		return nil, fmt.Errorf("world: life quest economy unavailable")
	}
	return s.fieldPackEconomy.Apply(ctx, questRewardIdentity(packID, quest, 0)+":life", nil, life)
}
