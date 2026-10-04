package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
	"fmt"
)

func packPurchaseIdentity(id int) string { return fmt.Sprintf("pack-purchase:%d", id) }

// EnsureInitialPackPurchase is called only by new-account initialization. It
// awards the bootstrap pack's real purchase rewards, not a recovery inference
// from a saved position or from the former seed-only unlock chain.
func (s *Service) EnsureInitialPackPurchase() error {
	_, err := s.purchaseStoryPack(s.startingPack(), true)
	return err
}

func (s *Service) handlePackBuy(request []byte) (int, []byte, bool, error) {
	id, err := requestPack(request)
	if err != nil {
		return 0, nil, true, err
	}
	if !s.packUnlocked(id) {
		return 0, nil, true, fmt.Errorf("%w: unavailable purchase pack %d", ErrInvalidRequest, id)
	}
	bundle, err := s.purchaseStoryPack(id, false)
	if err != nil {
		return 0, nil, true, err
	}
	var info []byte
	for _, row := range s.packDBInfoRows() {
		value, _, _ := wire.Varint(row, 1)
		if value == uint64(id) {
			info = row
			break
		}
	}
	if info == nil {
		return 0, nil, true, fmt.Errorf("world: purchased pack absent from account")
	}
	response := wire.AppendBytes(nil, 1, info)
	return 6, wire.AppendBytes(response, 2, bundle), true, nil
}

func (s *Service) grantPurchaseRewards(identity string, rewards []gamedata.Reward) ([]player.Item, error) {
	var itemRewards []gamedata.BattleReward
	for _, reward := range rewards {
		if reward.Count == 0 && reward.Type != 11 {
			return nil, fmt.Errorf("world: empty pack purchase reward")
		}
		if reward.Type == 19 {
			if reward.ID == 0 {
				return nil, fmt.Errorf("world: invalid pack ticket")
			}
			itemRewards = append(itemRewards, gamedata.BattleReward{Type: reward.Type, ID: reward.ID, Count: reward.Count})
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
		grant, err := s.collection.GrantCostumes(identity+":costumes", costumeIDs, s.questCostumes)
		if err != nil {
			return nil, err
		}
		var exchanges []gamedata.Reward
		for _, x := range grant.Exchanges {
			if x.ExchangeItemType != 20 {
				return nil, fmt.Errorf("world: unsupported purchase costume exchange")
			}
			exchanges = append(exchanges, gamedata.Reward{Type: x.ExchangeItemType, ID: x.ExchangeItemID, Count: x.ExchangeCount})
		}
		if len(exchanges) > 0 {
			if _, err := s.wallet.GrantQuestOnce(identity+":costumes:exchange", exchanges); err != nil {
				return nil, err
			}
		}
	}
	if _, err := s.wallet.GrantQuestOnce(identity+":currency", rewards); err != nil {
		return nil, err
	}
	if len(itemRewards) == 0 {
		return nil, nil
	}
	items, err := s.inventory.GrantOnce(identity+":items", itemRewards)
	if err == nil && len(items) == 0 {
		items = s.inventory.GrantedItems(identity + ":items")
	}
	return items, err
}

func (s *Service) purchaseStoryPack(id int, initial bool) ([]byte, error) {
	if s.storyCatalog == nil || s.collection == nil || s.wallet == nil || s.inventory == nil {
		return nil, fmt.Errorf("world: purchase services unavailable")
	}
	pack, exists := s.storyCatalog.Packs[id]
	if !exists {
		return nil, fmt.Errorf("%w: unknown purchase pack %d", ErrInvalidRequest, id)
	}
	buyRewards := pack.BuyRewards
	identity := packPurchaseIdentity(id)
	if _, owned := s.collection.Grant(identity); owned {
		return []byte{}, nil
	}
	if !initial && pack.BuyPrice != 0 {
		return nil, fmt.Errorf("%w: paid pack purchase type %d is unsupported", ErrInvalidRequest, pack.BuyType)
	}
	for _, reward := range buyRewards {
		if reward.Type != 3 && reward.Type != 4 && reward.Type != 12 && reward.Type != 19 && reward.Type != 11 {
			return nil, fmt.Errorf("world: unsupported pack purchase reward type %d", reward.Type)
		}
	}
	items, err := s.grantPurchaseRewards(identity, buyRewards)
	if err != nil {
		return nil, err
	}
	if err := s.collection.RecordGrantMarker(identity); err != nil {
		return nil, err
	}
	var bundle []byte
	if grant, found := s.collection.Grant(identity + ":costumes"); found {
		bundle = append(bundle, player.CollectionRewardBundle(s.collection, grant)...)
	}
	for _, reward := range buyRewards {
		if reward.Type == 3 || reward.Type == 4 || reward.Type == 12 {
			currency := wire.AppendVarint(wire.AppendVarint(nil, 3, reward.Type), 4, reward.Count)
			bundle = wire.AppendBytes(bundle, 1, currency)
		}
	}
	for _, item := range items {
		bundle = wire.AppendBytes(bundle, 1, player.ItemWire(item))
	}
	return bundle, nil
}
