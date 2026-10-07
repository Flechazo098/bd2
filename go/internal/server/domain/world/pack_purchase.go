package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"fmt"
)

func packPurchaseIdentity(id int) string { return fmt.Sprintf("pack-purchase:%d", id) }

func (s *Service) grantPurchaseRewards(ctx command.Context, identity string, rewards []gamedata.Reward) ([]assets.Item, error) {
	var itemRewards []gamedata.BattleReward
	for _, reward := range rewards {
		if reward.Count == 0 && reward.Type != 11 {
			return nil, fmt.Errorf("world: empty pack purchase reward")
		}
		if reward.Type == 19 {
			if reward.ID == 0 {
				return nil, fmt.Errorf("world: invalid pack ticket")
			}
			itemRewards = append(itemRewards, gamedata.BattleReward{Type: reward.Type, ID: reward.ID, Count: reward.Count}) //nolint:staticcheck // S1016
		}
	}
	var costumeIDs []uint64
	for _, reward := range rewards {
		if reward.Type == 11 {
			if s.questCostumes == nil {
				return nil, fmt.Errorf("world: missing purchase costume design")
			}
			if _, found := s.questCostumes.Character(reward.ID); !found {
				return nil, fmt.Errorf("world: missing purchase costume %d", reward.ID)
			}
			costumeIDs = append(costumeIDs, reward.ID)
		}
	}
	if len(costumeIDs) > 0 {
		grant, err := s.collection.GrantCostumes(ctx, identity+":costumes", costumeIDs, s.questCostumes)
		if err != nil {
			return nil, err
		}
		var exchanges []gamedata.Reward
		for _, x := range grant.Exchanges {
			if !purchaseCurrency(x.ExchangeItemType) || x.ExchangeItemID != 0 {
				return nil, fmt.Errorf("world: unsupported purchase costume exchange")
			}
			exchanges = append(exchanges, gamedata.Reward{Type: x.ExchangeItemType, ID: x.ExchangeItemID, Count: x.ExchangeCount})
		}
		if len(exchanges) > 0 {
			if _, err := s.wallet.GrantQuestOnce(ctx, identity+":costumes:exchange", exchanges); err != nil {
				return nil, err
			}
		}
	}
	if _, err := s.wallet.GrantQuestOnce(ctx, identity+":currency", rewards); err != nil {
		return nil, err
	}
	if len(itemRewards) == 0 {
		return nil, nil
	}
	items, err := s.inventory.GrantOnce(ctx, identity+":items", itemRewards)
	if err == nil && len(items) == 0 {
		items = s.inventory.GrantedItems(identity + ":items")
	}
	return items, err
}

func purchaseCurrency(typ uint64) bool {
	switch typ {
	case 2, 3, 4, 12, 20:
		return true
	}
	return false
}
