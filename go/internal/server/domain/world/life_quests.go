package world

import (
	"fmt"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

// Life packs carry one quest chain. Its current quest is always active: the
// farm HUD lists it, LifeQuestController reports progress with QuestUpdate,
// and QuestClear accepts the next quest like any non-main quest. The farm's
// first tutorial focuses that HUD entry, so without an active quest it can
// never reach the marker that records its completion.

func (s *Service) lifeQuestDesigns(packID int) map[int]gamedata.QuestDesign {
	chain := s.fieldPacks[packID].LifeQuests
	if len(chain) == 0 {
		return nil
	}
	quests := make(map[int]gamedata.QuestDesign, len(chain))
	for _, quest := range chain {
		quests[quest.ID] = quest
	}
	return quests
}

// ensureLifeQuest accepts the first quest of the chain that is not cleared.
func (s *Service) ensureLifeQuest(ctx command.Context, packID int) error {
	for _, quest := range s.fieldPacks[packID].LifeQuests {
		if s.state.QuestCleared(quest.ID, packID, 0) {
			continue
		}
		if _, active := s.state.QuestInPack(quest.ID, packID, 0); active {
			return nil
		}
		return s.state.AcceptQuest(ctx, quest.ID, packID, 0)
	}
	return nil
}

// lifeQuestWires are QuestDBInfo rows for the active Life quests. Their
// progress is QuestDBInfo.value, which LifeQuestController adds to.
func (s *Service) lifeQuestWires(packID int) [][]byte {
	var out [][]byte
	for _, quest := range s.fieldPacks[packID].LifeQuests {
		progress, active := s.state.QuestInPack(quest.ID, packID, 0)
		if !active || s.state.QuestCleared(quest.ID, packID, 0) {
			continue
		}
		row := wire.AppendVarint(nil, 1, uint64(quest.ID))
		if len(progress.Values) > 0 && progress.Values[0] > 0 {
			row = wire.AppendVarint(row, 2, uint64(progress.Values[0]))
		}
		out = append(out, wire.AppendVarint(row, 6, uint64(packID)))
	}
	return out
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
