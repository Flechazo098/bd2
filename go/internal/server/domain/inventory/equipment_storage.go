package inventory

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
)

func equipmentStorageChanges(before, after []Equipment) ([]stateio.EntryMutation, error) {
	old := map[uint64]Equipment{}
	for _, entry := range before {
		old[entry.InvenIndex] = entry
	}
	var changes []stateio.EntryMutation
	for _, entry := range after {
		previous, exists := old[entry.InvenIndex]
		if !exists || !reflect.DeepEqual(previous, entry) {
			raw, err := json.Marshal(entry)
			if err != nil {
				return nil, err
			}
			changes = append(changes, stateio.EntryMutation{Bucket: "storage", Key: strconv.FormatUint(entry.InvenIndex, 10), Payload: raw})
		}
		delete(old, entry.InvenIndex)
	}
	for index := range old {
		changes = append(changes, stateio.EntryMutation{Bucket: "storage", Key: strconv.FormatUint(index, 10), Delete: true})
	}
	return changes, nil
}

func (s *EquipmentInventory) loadEquipmentStorage(ctx command.Context) error {
	raw, err := s.store.ListEntries(ctx.State, "equipment", "storage")
	if err != nil {
		return err
	}
	seen := map[uint64]bool{}
	for _, entry := range s.owned.Equipment {
		seen[entry.InvenIndex] = true
	}
	for key, payload := range raw {
		var entry Equipment
		index, err := strconv.ParseUint(key, 10, 64)
		if err != nil || json.Unmarshal(payload, &entry) != nil || entry.InvenIndex != index || index == 0 || index >= s.owned.NextIndex || seen[index] || entry.ID == 0 || entry.UseChar != 0 || len(entry.Rank) != 3 {
			return fmt.Errorf("inventory: invalid stored equipment %q", key)
		}
		for _, rank := range entry.Rank {
			if rank > 4 {
				return errors.New("inventory: invalid stored equipment rank")
			}
		}
		seen[index] = true
		s.owned.Storage = append(s.owned.Storage, entry)
	}
	sort.Slice(s.owned.Storage, func(i, j int) bool { return s.owned.Storage[i].InvenIndex < s.owned.Storage[j].InvenIndex })
	return nil
}

func decodeEquipmentReference(request []byte) (uint64, uint64, error) {
	var index, id uint64
	found := false
	err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		if found || field.Type != 2 {
			return errors.New("inventory: invalid equipment reference")
		}
		found = true
		return wire.Walk(field.Value, func(field wire.Field) error {
			switch field.Number {
			case 1:
				if field.Type != 0 {
					return errors.New("inventory: invalid equipment index")
				}
				index, _ = binary.Uvarint(field.Value)
				return nil
			case 5:
				if field.Type != 2 {
					return errors.New("inventory: invalid equipment base reference")
				}
				return operationVarints(field.Value, map[int]*uint64{1: &id})
			}
			return nil
		})
	})
	if err != nil {
		return 0, 0, err
	}
	if !found || index == 0 || id == 0 {
		return 0, 0, errors.New("inventory: equipment reference missing")
	}
	return index, id, nil
}

func (s *EquipmentInventory) handleEquipmentStorage(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path == "/EquipStorageInfo" {
		var response []byte
		for _, entry := range s.owned.Storage {
			response = wire.AppendBytes(response, 1, EquipmentWire(entry))
		}
		return 81, response, true, nil
	}
	index, id, err := decodeEquipmentReference(request)
	if err != nil {
		return 0, nil, true, err
	}
	if s.capacity == nil {
		return 0, nil, true, errors.New("inventory: equipment storage capacities unavailable")
	}
	counts, err := s.capacity.InventorySlotCounts(ctx)
	if err != nil {
		return 0, nil, true, err
	}
	next := cloneEquipmentSnapshot(s.owned)
	source, target := &next.Equipment, &next.Storage
	code, capacity := 79, counts.EquipmentStorage
	if path == "/EquipStorageOut" {
		source, target = &next.Storage, &next.Equipment
		code, capacity = 80, counts.Equipment
	}
	at := slices.IndexFunc(*source, func(entry Equipment) bool { return entry.InvenIndex == index })
	if at < 0 {
		return 0, nil, true, errors.New("inventory: equipment not in requested inventory")
	}
	entry := (*source)[at]
	if entry.ID != id || entry.UseChar != 0 || (s.pendingReroll != nil && s.pendingReroll.Equipment.InvenIndex == index) {
		return 0, nil, true, errors.New("inventory: equipment identity, assignment or pending operation mismatch")
	}
	if uint64(len(*target)) >= capacity {
		return 0, nil, true, errors.New("inventory: equipment destination capacity exceeded")
	}
	*source = slices.Delete(*source, at, at+1)
	*target = append(*target, entry)
	if err := s.commitLocked(ctx, next, "storage move"); err != nil {
		return 0, nil, true, err
	}
	return code, wire.AppendBytes(nil, 1, EquipmentWire(entry)), true, nil
}

func (s *EquipmentInventory) Discard(ctx command.Context, want Item) error {
	at := s.equipmentPositionLocked(want.InvenIndex)
	if at < 0 {
		return errors.New("inventory: equipment to discard not found")
	}
	entry := s.owned.Equipment[at]
	if want.Type != 10 || want.Count != 1 || want.ID != entry.ID || entry.UseChar != 0 || entry.LockFlag != 0 || s.upgrade == nil || s.upgrade.NotTrash[entry.ID] || (s.pendingReroll != nil && s.pendingReroll.Equipment.InvenIndex == entry.InvenIndex) {
		return errors.New("inventory: equipment cannot be discarded")
	}
	next := cloneEquipmentSnapshot(s.owned)
	next.Equipment = slices.Delete(next.Equipment, at, at+1)
	return s.commitLocked(ctx, next, "discard")
}

func (s *EquipmentInventory) removeAbsentPresetEquipment(ctx command.Context, next equipmentSnapshot) error {
	present := map[uint64]bool{}
	for _, entry := range next.Equipment {
		present[entry.InvenIndex] = true
	}
	for key, preset := range s.presets {
		copy := cloneEquipmentPreset(preset)
		changed := false
		for i, item := range copy.Items {
			if item.EquipmentIndex != 0 && !present[item.EquipmentIndex] {
				copy.Items[i].EquipmentIndex = 0
				changed = true
			}
		}
		if changed {
			if err := s.persistEquipmentPreset(ctx, copy, "removed equipment"); err != nil {
				return err
			}
			s.presets[key] = copy
		}
	}
	return nil
}
