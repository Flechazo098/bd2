package world

import (
	"bd2server/internal/server/wire"
	"fmt"
	"sort"
)

func (s *Service) fieldObjectCurrentPack(pack int) bool {
	s.activePackMu.RLock()
	current := s.activePack
	s.activePackMu.RUnlock()
	if current == 0 {
		current = s.state.ActivePackID()
	}
	return current == pack
}

func (s *Service) validateFieldObjectMap(pack, mapID int) error {
	_, saved := s.state.Position()
	_, event, err := s.resolveEventFieldPack(pack)
	if err != nil {
		return err
	}
	if !saved && !event {
		return nil
	}
	current, err := s.currentFieldMap(pack)
	if err != nil || current != mapID {
		return fmt.Errorf("%w: field object outside current map", ErrInvalidRequest)
	}
	return nil
}

func fieldObjectInt(raw []byte, field int, required bool) (int, error) {
	v, found, err := wire.Varint(raw, field)
	if err != nil || required && (!found || v == 0) || v > uint64(^uint32(0)>>1) {
		return 0, ErrInvalidRequest
	}
	return int(v), nil
}

// The client batches LostCoin collections. Validate the complete list before
// drawing or settling any component; the dispatcher commits the whole request.
func (s *Service) handleFieldObjectRewardList(request []byte) (int, []byte, bool, error) {
	pack, err := requestPack(request)
	if err != nil {
		return 260, nil, true, err
	}
	design, err := s.fieldObjectDesign(pack)
	if err != nil {
		return 260, nil, true, err
	}
	type selection struct{ group, id int }
	var selections []selection
	seen := map[int]bool{}
	err = wire.Walk(request, func(f wire.Field) error {
		if f.Number != 3 {
			return nil
		}
		if f.Type != 2 || len(selections) >= 100 {
			return ErrInvalidRequest
		}
		group, e := fieldObjectInt(f.Value, 3, true)
		if e != nil {
			return e
		}
		id, e := fieldObjectInt(f.Value, 4, true)
		if e != nil {
			return e
		}
		obj, ok := design.Objects[id]
		if !ok || obj.GroupID != group || obj.BuffID != 0 || obj.MonsterID != 0 || len(obj.Rewards) == 0 && obj.Type != 6 {
			return ErrInvalidRequest
		}
		if !seen[id] {
			seen[id] = true
			selections = append(selections, selection{group, id})
		}
		return nil
	})
	if err != nil || len(selections) == 0 || !s.packUnlocked(pack) || !s.fieldObjectCurrentPack(pack) {
		return 260, nil, true, ErrInvalidRequest
	}
	var bundle []byte
	for _, selection := range selections {
		part, e := s.openFieldObject(pack, selection.group, selection.id)
		if e != nil {
			return 260, nil, true, e
		}
		bundle = append(bundle, part...)
	}
	return 260, wire.AppendBytes(nil, 1, bundle), true, nil
}

func (s *Service) handleFieldObjectPreview(request []byte) (int, []byte, bool, error) {
	pack, err := requestPack(request)
	if err != nil || !s.packUnlocked(pack) {
		return 144, nil, true, ErrInvalidRequest
	}
	ids, err := s.openedFieldObjects(pack)
	if err != nil {
		return 144, nil, true, err
	}
	var response []byte
	for _, id := range ids {
		response = wire.AppendBytes(response, 1, wire.AppendVarint(nil, 1, uint64(id)))
	}
	research, err := s.state.ResearchObjects(pack)
	if err != nil {
		return 144, nil, true, err
	}
	for _, id := range research {
		response = wire.AppendVarint(response, 2, uint64(id))
	}
	return 144, response, true, nil
}

// Respawn is a projection of the reset schedule, never an instruction to clear
// receipts. A request before the reset cannot make an object collectible again.
func (s *Service) handleFieldObjectRespawn(request []byte) (int, []byte, bool, error) {
	pack, err := requestPack(request)
	if err != nil || !s.packUnlocked(pack) {
		return 30, nil, true, ErrInvalidRequest
	}
	group, err := fieldObjectInt(request, 3, true)
	if err != nil {
		return 30, nil, true, err
	}
	kind, err := fieldObjectInt(request, 4, false)
	if err != nil || kind != 0 && kind != 6 {
		return 30, nil, true, ErrInvalidRequest
	}
	design, err := s.fieldObjectDesign(pack)
	if err != nil {
		return 30, nil, true, err
	}
	var ids []int
	reset := -1
	for id, obj := range design.Objects {
		if obj.GroupID == group {
			ids = append(ids, id)
			if reset != -1 && reset != obj.ResetType {
				return 30, nil, true, fmt.Errorf("world: inconsistent field reset group")
			}
			reset = obj.ResetType
		}
	}
	if len(ids) == 0 {
		return 30, nil, true, ErrInvalidRequest
	}
	sort.Ints(ids)
	var response []byte
	for _, id := range ids {
		obj := design.Objects[id]
		period, e := s.fieldObjectPeriodFor(pack, obj)
		if e != nil {
			return 30, nil, true, e
		}
		opened, e := s.state.FieldRewardOpened(pack, id, period)
		if e != nil {
			return 30, nil, true, e
		}
		if opened {
			response = wire.AppendBytes(response, 1, wire.AppendVarint(nil, 1, uint64(id)))
		}
	}
	if reset == 0 || reset == 3 {
		next, e := s.fieldReset.Next(reset, s.monsterTime())
		if e != nil {
			return 30, nil, true, e
		}
		row := wire.AppendVarint(nil, 1, uint64(group))
		row = wire.AppendVarint(row, 2, uint64(next.UnixMilli()))
		response = wire.AppendBytes(response, 2, row)
	} else if reset == 2 {
		resolver, ok := s.eventFieldPacks.(interface {
			FieldObjectEventPeriod(int) (string, int64, error)
		})
		if !ok {
			return 30, nil, true, ErrInvalidRequest
		}
		_, end, err := resolver.FieldObjectEventPeriod(pack)
		if err != nil {
			return 30, nil, true, err
		}
		row := wire.AppendVarint(wire.AppendVarint(nil, 1, uint64(group)), 2, uint64(end))
		response = wire.AppendBytes(response, 2, row)
	}
	return 30, response, true, nil
}

func (s *Service) rewardMonsterAvailable(pack, monster int) (bool, error) {
	if s.fieldObjectLoader == nil && s.fieldObjects == nil {
		return true, nil
	}
	design, err := s.fieldObjectDesign(pack)
	if err != nil {
		return false, err
	}
	linked := false
	for _, obj := range design.Objects {
		if obj.MonsterID != monster || obj.BuffID != 0 || len(obj.Rewards) != 0 {
			continue
		}
		linked = true
		period, e := s.fieldObjectPeriodFor(pack, obj)
		if e != nil {
			continue
		}
		opened, e := s.state.FieldRewardOpened(pack, obj.ID, period)
		if e != nil {
			return false, e
		}
		if opened {
			return true, nil
		}
	}
	return !linked, nil
}
