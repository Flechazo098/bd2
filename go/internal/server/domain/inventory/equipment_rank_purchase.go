package inventory

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"errors"
	"fmt"
)

func (s *EquipmentInventory) AttachRankPurchases(design *gamedata.EquipmentRankPurchaseDesign, charge func(command.Context, string, gamedata.CashProductKey) error) error {
	if design == nil || charge == nil {
		return errors.New("inventory: missing rank purchase dependencies")
	}
	s.rankPurchases, s.chargeRankPurchase = design, charge
	return nil
}

func (s *EquipmentInventory) rankUpgradeBuy(ctx command.Context, request []byte) (int, []byte, bool, error) {
	var seq, index, group, product uint64
	if err := operationVarints(request, map[int]*uint64{1: &seq, 2: &index, 3: &group, 4: &product}); err != nil {
		return 0, nil, true, err
	}
	if s.rankPurchases == nil || s.chargeRankPurchase == nil || s.smelting == nil || index == 0 {
		return 0, nil, true, errors.New("inventory: rank purchase unavailable or equipment missing")
	}
	offer, exists := s.rankPurchases.Products[[2]uint64{group, product}]
	if !exists {
		return 0, nil, true, errors.New("inventory: unknown equipment rank product")
	}
	at := s.equipmentPositionLocked(index)
	if at < 0 {
		return 0, nil, true, errors.New("inventory: rank equipment not found")
	}
	entry := s.owned.Equipment[at]
	if !s.rankPurchases.Eligible[entry.ID] || entry.Level < 9 || (s.pendingReroll != nil && s.pendingReroll.Equipment.InvenIndex == index) {
		return 0, nil, true, errors.New("inventory: equipment not eligible for rank purchase")
	}
	current, err := s.smelting.Score(entry.ID, entry.Rank)
	if err != nil {
		return 0, nil, true, err
	}
	target, err := s.smelting.Score(entry.ID, offer.Ranks)
	if err != nil {
		return 0, nil, true, err
	}
	if current >= target {
		return 0, nil, true, errors.New("inventory: rank purchase does not increase score")
	}
	var costs []Item
	err = wire.Walk(request, func(field wire.Field) error {
		if field.Number != 5 {
			return nil
		}
		if field.Type != 2 {
			return errors.New("inventory: invalid rank price")
		}
		var cost Item
		if err := operationVarints(field.Value, map[int]*uint64{1: &cost.InvenIndex, 2: &cost.ID, 3: &cost.Type, 4: &cost.Count}); err != nil {
			return err
		}
		costs = append(costs, cost)
		return nil
	})
	if err != nil {
		return 0, nil, true, err
	}
	if len(costs) != 1 || costs[0].InvenIndex != 0 || costs[0].ID != offer.PriceID || costs[0].Type != offer.PriceType || costs[0].Count != offer.PriceCount {
		return 0, nil, true, errors.New("inventory: rank purchase price mismatch")
	}
	identity := fmt.Sprintf("equip-rank-buy:%s:%s:%d:%d", ctx.SessionID, ctx.RequestID, seq, index)
	if err := s.chargeRankPurchase(ctx, identity, offer.Key); err != nil {
		return 0, nil, true, err
	}
	next := cloneEquipmentSnapshot(s.owned)
	next.Equipment[at].Rank = append([]uint64(nil), offer.Ranks...)
	if err := s.commitLocked(ctx, next, "rank purchase"); err != nil {
		return 0, nil, true, err
	}
	var response []byte
	for _, rank := range offer.Ranks {
		response = wire.AppendVarint(response, 1, rank)
	}
	if character, found := s.equippedCharacter(ctx, next.Equipment[at]); found {
		response = wire.AppendBytes(response, 2, character.Response)
	}
	return 579, response, true, nil
}
