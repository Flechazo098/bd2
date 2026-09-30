package gacha

import (
	"errors"
	"fmt"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
)

const firstGachaResponseDigest = "first-gacha-confirm-v1"

type firstGachaPreview struct {
	sequence  uint64
	rewards   []gamedata.FirstGachaReward
	equipment map[int]player.Equipment
	response  []byte
}

func (s *Service) previewFirstGacha(request []byte) (int, []byte, bool, error) {
	if s.first == nil {
		return 175, nil, true, errors.New("gacha: first gacha is not configured")
	}
	if s.FirstGachaCompleted() {
		return 175, nil, true, errors.New("gacha: first gacha already completed")
	}
	for _, field := range []int{3, 4} {
		value, _, err := wire.Varint(request, field)
		if err != nil || value != 0 {
			return 175, nil, true, errors.New("gacha: first gacha preview cannot use a cash product")
		}
	}
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return 175, nil, true, errors.New("gacha: invalid first gacha preview sequence")
	}
	session := s.loginIdentity()
	s.firstMu.Lock()
	defer s.firstMu.Unlock()
	if previous, found := s.firstPreviews[session]; found && previous.sequence == seq {
		return 175, append([]byte(nil), previous.response...), true, nil
	}
	rewards, err := s.first.Roll()
	if err != nil {
		return 175, nil, true, err
	}
	preview := firstGachaPreview{sequence: seq, rewards: append([]gamedata.FirstGachaReward(nil), rewards...), equipment: make(map[int]player.Equipment)}
	var bundle []byte
	for sortID, reward := range rewards {
		switch reward.Type {
		case 11:
			costume := wire.AppendVarint(nil, 2, reward.ID)
			if sortID != 0 {
				costume = wire.AppendVarint(costume, 6, uint64(sortID))
			}
			bundle = wire.AppendBytes(bundle, 3, costume)
		case 10:
			entry, err := firstGachaEquipment(s.first.EquipmentCatalog(), reward.ID, uint64(sortID))
			if err != nil {
				return 175, nil, true, err
			}
			preview.equipment[sortID] = entry
			bundle = wire.AppendBytes(bundle, 4, player.EquipmentWire(entry))
		default:
			return 175, nil, true, fmt.Errorf("gacha: unsupported first reward type %d", reward.Type)
		}
	}
	preview.response = wire.AppendBytes(nil, 1, bundle)
	s.firstPreviews[session] = preview
	if s.onPreview != nil {
		if err := s.onPreview(); err != nil {
			delete(s.firstPreviews, session)
			return 175, nil, true, fmt.Errorf("gacha: update first preview mission: %w", err)
		}
	}
	return 175, append([]byte(nil), preview.response...), true, nil
}

func firstGachaEquipment(catalog *gamedata.EquipmentGachaCatalog, id, sortID uint64) (player.Equipment, error) {
	if catalog == nil {
		return player.Equipment{}, errors.New("gacha: first gacha equipment catalog missing")
	}
	main, sub, private, err := catalog.RollOptions(id)
	if err != nil {
		return player.Equipment{}, err
	}
	entry := player.Equipment{ID: id, SortID: sortID, Rank: []uint64{0, 0, 0}}
	for _, option := range main {
		entry.MainOption = append(entry.MainOption, player.EquipmentOption{GroupID: option.GroupID, ID: option.ID})
	}
	for _, option := range sub {
		entry.SubOption = append(entry.SubOption, player.EquipmentOption{GroupID: option.GroupID, ID: option.ID})
	}
	if private != nil {
		entry.PrivateOption = &player.EquipmentOption{GroupID: private.GroupID, ID: private.ID}
	}
	return entry, nil
}

func (s *Service) confirmFirstGacha(_ []byte, seq, buyType uint64, tickets []player.Item, explicitIdentity string) (int, []byte, bool, error) {
	if s.first == nil || s.equipmentInventory == nil {
		return 146, nil, true, errors.New("gacha: first gacha runtime is not attached")
	}
	if buyType != 1 || len(tickets) != 0 {
		return 146, nil, true, errors.New("gacha: first gacha requires normal free confirmation")
	}
	identity := explicitIdentity
	if identity == "" {
		identity = s.requestIdentity(s.first.GachaID, seq)
	}
	responseIdentity := identity + ":response"
	if response, found, err := s.collection.GachaBatchResponse(responseIdentity, firstGachaResponseDigest); found || err != nil {
		return 146, response, true, err
	}
	if s.FirstGachaCompleted() {
		return 146, nil, true, errors.New("gacha: first gacha already completed")
	}
	session := s.loginIdentity()
	s.firstMu.Lock()
	preview, found := s.firstPreviews[session]
	s.firstMu.Unlock()
	if !found || len(preview.rewards) != s.first.Count {
		return 146, nil, true, errors.New("gacha: first gacha confirmation requires a preview")
	}
	var costumeIDs, costumeSortIDs []uint64
	var equipment []player.Equipment
	for sortID, reward := range preview.rewards {
		switch reward.Type {
		case 11:
			costumeIDs = append(costumeIDs, reward.ID)
			costumeSortIDs = append(costumeSortIDs, uint64(sortID))
		case 10:
			candidate, ok := preview.equipment[sortID]
			if !ok || candidate.ID != reward.ID {
				return 146, nil, true, fmt.Errorf("gacha: first preview missing equipment slot %d", sortID)
			}
			saved, err := s.equipmentInventory.GrantGeneratedOnce(fmt.Sprintf("%s:first-equip:%d", identity, sortID), candidate)
			if err != nil {
				return 146, nil, true, err
			}
			equipment = append(equipment, saved)
		default:
			return 146, nil, true, fmt.Errorf("gacha: unsupported first reward type %d", reward.Type)
		}
	}
	grant, err := s.collection.GrantRegularPurchase(identity, costumeIDs, s.first.CostumeCatalog(), player.GachaPurchase{
		Group: s.first.Group, BuyType: 1, RewardCount: uint64(s.first.Count), RewardSortIDs: costumeSortIDs,
	})
	if err != nil {
		return 146, nil, true, err
	}
	if err := s.creditOverflow(identity, grant); err != nil {
		return 146, nil, true, err
	}
	bundle := s.rewardBundle(grant)
	for _, entry := range equipment {
		bundle = wire.AppendBytes(bundle, 4, player.EquipmentWire(entry))
	}
	response := wire.AppendBytes(nil, 1, bundle)
	if err := s.collection.RecordGachaBatch(responseIdentity, firstGachaResponseDigest, response); err != nil {
		return 146, nil, true, err
	}
	if s.onPreview != nil {
		if err := s.onPreview(); err != nil {
			return 146, nil, true, fmt.Errorf("gacha: update first gacha mission: %w", err)
		}
	}
	return 146, response, true, nil
}
