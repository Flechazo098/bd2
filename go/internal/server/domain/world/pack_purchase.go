package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"fmt"
)

func packPurchaseIdentity(id int) string { return fmt.Sprintf("pack-purchase:%d", id) }
func packEntryIdentity(id int) string    { return fmt.Sprintf("pack-entered:%d", id) }

// EnsureInitialPackPurchase awards the configured starting card only during
// account initialization. It does not infer ownership from a saved position.
func (s *Service) EnsureInitialPackPurchase(ctx command.Context) error {
	pack, err := s.resolvePack(ctx, s.startingPack())
	if err != nil {
		return err
	}
	_, err = s.purchaseResolvedPack(ctx, pack, true)
	return err
}

func (s *Service) handlePackBuy(ctx command.Context, request []byte) (int, []byte, bool, error) {
	id, err := requestPack(request)
	if err != nil {
		return 6, nil, true, err
	}
	pack, err := s.resolvePack(ctx, id)
	if err != nil {
		return 6, nil, true, err
	}
	if !s.packRequirementsMet(ctx, pack) {
		return 6, nil, true, fmt.Errorf("%w: unavailable purchase pack %d", ErrInvalidRequest, id)
	}
	bundle, err := s.purchaseResolvedPack(ctx, pack, false)
	if err != nil {
		return 6, nil, true, err
	}
	var info []byte
	switch pack.kind {
	case eventCard:
		info = s.eventPackDBInfo(pack.event)
	case fieldCard:
		if pack.field.Type != 5 {
			info = s.fieldPackDBInfo(pack.field)
		}
	}
	if info == nil {
		rows, err := s.packDBInfoRows(ctx)
		if err != nil {
			return 6, nil, true, err
		}
		for _, row := range rows {
			value, _, _ := wire.Varint(row, 1)
			if value == uint64(id) {
				info = row
				break
			}
		}
	}
	if info == nil {
		return 6, nil, true, fmt.Errorf("world: purchased pack %d absent from account", id)
	}
	return 6, wire.AppendBytes(wire.AppendBytes(nil, 1, info), 2, bundle), true, nil
}

// All card purchase paths share one receipt. The enclosing account transaction
// commits the price, rewards and ownership together.
func (s *Service) purchaseResolvedPack(ctx command.Context, pack packDefinition, initial bool) ([]byte, error) {
	if s.collection == nil {
		return nil, fmt.Errorf("world: pack purchase collection unavailable")
	}
	if s.packPurchased(pack.id) {
		return []byte{}, nil
	}
	buyType, buyPrice, rewards := pack.purchaseTerms()
	identity := packPurchaseIdentity(pack.id)
	var bundle []byte
	var err error
	if pack.kind == fieldCard {
		economy := s.fieldPackEconomy
		if pack.field.Type == 5 {
			if s.skyway == nil {
				return nil, fmt.Errorf("world: SkyWay purchase runtime unavailable")
			}
			economy = s.skyway.economy
		}
		if economy == nil {
			return nil, fmt.Errorf("world: field pack purchase economy unavailable")
		}
		var costs []gamedata.Reward
		if !initial && buyPrice != 0 {
			costs = []gamedata.Reward{{Type: buyType, Count: buyPrice}}
		}
		bundle, err = economy.Apply(ctx, identity, costs, rewards)
	} else {
		bundle, err = s.applyStoryPackPurchase(ctx, pack.id, buyType, buyPrice, rewards, initial)
	}
	if err != nil {
		return nil, err
	}
	if err := s.collection.RecordGrantMarker(ctx, identity); err != nil {
		return nil, err
	}
	return bundle, nil
}

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

func (s *Service) applyStoryPackPurchase(ctx command.Context, id int, buyType, buyPrice uint64, buyRewards []gamedata.Reward, initial bool) ([]byte, error) {
	if s.wallet == nil || s.inventory == nil {
		return nil, fmt.Errorf("world: purchase services unavailable")
	}
	identity := packPurchaseIdentity(id)
	for _, reward := range buyRewards {
		if !purchaseCurrency(reward.Type) && reward.Type != 19 && reward.Type != 11 {
			return nil, fmt.Errorf("world: unsupported pack purchase reward type %d", reward.Type)
		}
	}
	if !initial && buyPrice != 0 {
		var err error
		switch buyType {
		case 2:
			_, err = s.wallet.SpendJewelryOnce(ctx, identity+":price", buyPrice)
		case 3:
			_, err = s.wallet.SpendFreeJewelryOnce(ctx, identity+":price", buyPrice)
		case 4:
			_, err = s.wallet.SpendGoldOnce(ctx, identity+":price", buyPrice)
		case 12:
			_, err = s.wallet.SpendCatalystOnce(ctx, identity+":price", buyPrice)
		default:
			return nil, fmt.Errorf("%w: unsupported pack purchase currency%d", ErrInvalidRequest, buyType)
		}
		if err != nil {
			return nil, err
		}
	}
	items, err := s.grantPurchaseRewards(ctx, identity, buyRewards)
	if err != nil {
		return nil, err
	}
	var bundle []byte
	if grant, found := s.collection.Grant(identity + ":costumes"); found {
		bundle = append(bundle, roster.CollectionRewardBundle(s.collection, grant)...)
	}
	for _, reward := range buyRewards {
		if purchaseCurrency(reward.Type) {
			currency := wire.AppendVarint(wire.AppendVarint(nil, 3, reward.Type), 4, reward.Count)
			bundle = wire.AppendBytes(bundle, 1, currency)
		}
	}
	for _, item := range items {
		bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(item))
	}
	return bundle, nil
}
