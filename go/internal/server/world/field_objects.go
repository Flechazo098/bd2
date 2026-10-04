package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
	"fmt"
	"time"
)

func (s *Service) WithFieldObjects(designs map[int]gamedata.FieldObjectDesign) *Service {
	s.fieldObjects = designs
	return s
}
func (s *Service) AttachFieldObjectRuntime(root, version string) error {
	schedule, err := gamedata.LoadFieldResetSchedule(root, version)
	if err != nil {
		return err
	}
	s.fieldReset = schedule
	s.fieldObjectLoader = func(pack int) (gamedata.FieldObjectDesign, error) {
		return gamedata.LoadFieldObjects(root, version, pack)
	}
	s.fieldObjects = map[int]gamedata.FieldObjectDesign{}
	return nil
}
func (s *Service) fieldObjectDesign(pack int) (gamedata.FieldObjectDesign, error) {
	if design, ok := s.fieldObjects[pack]; ok {
		return design, nil
	}
	if s.fieldObjectLoader == nil {
		return gamedata.FieldObjectDesign{}, fmt.Errorf("world: field object design unavailable")
	}
	if _, story := s.packs[pack]; !story {
		if _, field := s.fieldPacks[pack]; !field {
			return gamedata.FieldObjectDesign{}, fmt.Errorf("%w: unknown field pack", ErrInvalidRequest)
		}
	}
	design, err := s.fieldObjectLoader(pack)
	if err != nil {
		return design, err
	}
	s.fieldObjects[pack] = design
	return design, nil
}
func (s *Service) openedFieldObjects(pack int) ([]int, error) {
	ids, err := s.state.OpenedFieldRewards(pack, s.questDifficulty(pack))
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []int{}, nil
	}
	design, err := s.fieldObjectDesign(pack)
	if err != nil {
		return nil, err
	}
	var active []int
	for _, id := range ids {
		obj, ok := design.Objects[id]
		if !ok {
			return nil, fmt.Errorf("world: saved field object absent from design")
		}
		period, e := s.fieldObjectPeriod(obj)
		if e != nil {
			continue
		}
		opened, e := s.state.FieldRewardOpened(pack, s.questDifficulty(pack), id, period)
		if e != nil {
			return nil, e
		}
		if opened {
			active = append(active, id)
		}
	}
	return active, nil
}
func (s *Service) WithFieldResetSchedule(schedule gamedata.FieldResetSchedule) *Service {
	s.fieldReset = schedule
	return s
}
func (s *Service) fieldObjectPeriod(obj gamedata.FieldRewardObject) (string, error) {
	return s.fieldReset.Period(obj.ResetType, time.Now())
}
func (s *Service) handleFieldObjectInfo(request []byte) (int, []byte, bool, error) {
	pack, err := requestPack(request)
	if err != nil {
		return 0, nil, true, err
	}
	if !s.packUnlocked(pack) {
		return 0, nil, true, fmt.Errorf("%w: unavailable field pack", ErrInvalidRequest)
	}
	ids, err := s.openedFieldObjects(pack)
	if err != nil {
		return 0, nil, true, err
	}
	var response []byte
	for _, id := range ids {
		response = wire.AppendBytes(response, 1, wire.AppendVarint(nil, 1, uint64(id)))
	}
	return 28, response, true, nil
}
func (s *Service) handleFieldObjectReward(request []byte) (int, []byte, bool, error) {
	pack, err := requestPack(request)
	if err != nil {
		return 0, nil, true, err
	}
	group, _, err := wire.Varint(request, 3)
	if err != nil || group == 0 || group > uint64(^uint32(0)>>1) {
		return 0, nil, true, ErrInvalidRequest
	}
	id, _, err := wire.Varint(request, 4)
	if err != nil || id == 0 || id > uint64(^uint32(0)>>1) {
		return 0, nil, true, ErrInvalidRequest
	}
	bundle, err := s.openFieldObject(pack, int(group), int(id))
	if err != nil {
		return 0, nil, true, err
	}
	return 29, wire.AppendBytes(nil, 1, bundle), true, nil
}
func (s *Service) openFieldObject(pack, group, id int) ([]byte, error) {
	design, err := s.fieldObjectDesign(pack)
	if err != nil {
		return nil, err
	}
	obj, exists := design.Objects[id]
	if !exists || obj.GroupID != group || !s.packUnlocked(pack) || s.state.ActivePackID() != pack {
		return nil, fmt.Errorf("%w: unavailable field object", ErrInvalidRequest)
	}
	if position, ok := s.state.Position(); ok && (position.PackID != pack || position.Position.MapID != obj.MapID) {
		return nil, fmt.Errorf("%w: field object outside current map", ErrInvalidRequest)
	}
	if obj.Type < 1 || obj.Type > 3 || obj.BuffID != 0 || obj.MonsterID != 0 || obj.QuestID != 0 || len(obj.Rewards) == 0 {
		return nil, fmt.Errorf("%w: unsupported field object reward graph/reset", ErrInvalidRequest)
	}
	difficulty := s.questDifficulty(pack)
	period, err := s.fieldObjectPeriod(obj)
	if err != nil {
		return nil, err
	}
	opened, err := s.state.FieldRewardOpened(pack, difficulty, id, period)
	if err != nil {
		return nil, err
	}
	if opened {
		return []byte{}, nil
	}
	if s.wallet == nil || s.inventory == nil {
		return nil, fmt.Errorf("world: field reward stores unavailable")
	}
	var rewards []gamedata.Reward
	var itemRewards []gamedata.BattleReward
	selected, err := obj.Draw()
	if err != nil {
		return nil, err
	}
	for _, r := range selected {
		if r.Count == 0 {
			return nil, fmt.Errorf("world: empty field reward")
		}
		switch r.Type {
		case 2, 3, 4, 12, 20:
			rewards = append(rewards, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count})
		case 5, 7, 8, 9, 13, 14, 17, 19, 27, 29:
			if r.ID == 0 {
				return nil, fmt.Errorf("world: invalid field item")
			}
			itemRewards = append(itemRewards, r)
		default:
			return nil, fmt.Errorf("%w: unsupported field reward type %d", ErrInvalidRequest, r.Type)
		}
	}
	identity := fmt.Sprintf("field-reward:%d:%d:%d:%s", pack, difficulty, id, period)
	if _, err = s.wallet.GrantQuestOnce(identity, rewards); err != nil {
		return nil, err
	}
	items, err := s.inventory.GrantOnce(identity, itemRewards)
	if err != nil {
		return nil, err
	}
	if err = s.state.MarkFieldRewardOpened(pack, difficulty, id, period); err != nil {
		return nil, err
	}
	var bundle []byte
	for _, r := range rewards {
		bundle = wire.AppendBytes(bundle, 1, player.ItemWire(player.Item{ID: r.ID, Type: r.Type, Count: r.Count}))
	}
	for _, item := range items {
		bundle = wire.AppendBytes(bundle, 1, player.ItemWire(item))
	}
	return bundle, nil
}
