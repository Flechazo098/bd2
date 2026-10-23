package inventory

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
)

func (s *Inventory) AttachCapacity(capacity *InventorySlots)          { s.capacity = capacity }
func (s *EquipmentInventory) AttachCapacity(capacity *InventorySlots) { s.capacity = capacity }
func (s *Inventory) AttachDiscard(kind uint64, discard func(command.Context, Item) error) {
	if s.discard == nil {
		s.discard = map[uint64]func(command.Context, Item) error{}
	}
	s.discard[kind] = discard
}

func itemEntryChanges(bucket string, before, after []Item) ([]stateio.EntryMutation, error) {
	old := map[uint64]Item{}
	for _, entry := range before {
		old[entry.InvenIndex] = entry
	}
	var changes []stateio.EntryMutation
	for _, entry := range after {
		previous, exists := old[entry.InvenIndex]
		if !exists || !equalItem(previous, entry) {
			raw, err := json.Marshal(entry)
			if err != nil {
				return nil, err
			}
			changes = append(changes, stateio.EntryMutation{Bucket: bucket, Key: strconv.FormatUint(entry.InvenIndex, 10), Payload: raw})
		}
		delete(old, entry.InvenIndex)
	}
	for index := range old {
		changes = append(changes, stateio.EntryMutation{Bucket: bucket, Key: strconv.FormatUint(index, 10), Delete: true})
	}
	return changes, nil
}

func (s *Inventory) loadItemStorage(ctx command.Context) error {
	raw, err := s.store.ListEntries(ctx.State, "items", "storage")
	if err != nil {
		return err
	}
	seen := map[uint64]bool{}
	for _, item := range s.owned.Items {
		seen[item.InvenIndex] = true
	}
	for _, item := range s.starter {
		seen[item.InvenIndex] = true
	}
	for key, payload := range raw {
		var item Item
		index, err := strconv.ParseUint(key, 10, 64)
		if err != nil || json.Unmarshal(payload, &item) != nil || index != item.InvenIndex || index == 0 || seen[index] || index >= 1<<59 || item.ID == 0 || item.Type == 0 || item.Count == 0 || item.KeepFlag > 1 {
			return fmt.Errorf("inventory: invalid storage item %q", key)
		}
		seen[index] = true
		s.owned.Storage = append(s.owned.Storage, item)
	}
	sort.Slice(s.owned.Storage, func(i, j int) bool { return s.owned.Storage[i].InvenIndex < s.owned.Storage[j].InvenIndex })
	return nil
}

func decodeItemOperation(request []byte) (Item, error) {
	var item Item
	found := false
	err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		if found || field.Type != 2 {
			return errors.New("inventory: invalid item operation payload")
		}
		found = true
		return operationVarints(field.Value, map[int]*uint64{1: &item.InvenIndex, 2: &item.ID, 3: &item.Type, 4: &item.Count})
	})
	if err != nil {
		return Item{}, err
	}
	if !found || item.InvenIndex == 0 || item.ID == 0 || item.Type == 0 || item.Count == 0 || item.Count > math.MaxInt32 {
		return Item{}, errors.New("inventory: invalid item operation")
	}
	return item, nil
}

func (s *Inventory) handleItemOperations(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return 0, nil, true, errors.New("inventory: missing sequence")
	}
	if path == "/ItemStorageInfo" {
		var out []byte
		for _, item := range s.owned.Storage {
			out = wire.AppendBytes(out, 1, ItemWire(item))
		}
		return 78, out, true, nil
	}
	if path == "/ItemLock" {
		return s.itemLock(ctx, request)
	}
	item, err := decodeItemOperation(request)
	if err != nil {
		return 0, nil, true, err
	}
	if path == "/ItemDump" {
		if discard := s.discard[item.Type]; discard != nil {
			if err := discard(ctx, item); err != nil {
				return 0, nil, true, err
			}
			return 23, nil, true, nil
		}
		if s.itemStacks == nil || !s.itemStacks.Discardable[[2]uint64{item.Type, item.ID}] {
			return 0, nil, true, errors.New("inventory: item cannot be discarded")
		}
		next := cloneOwnedSnapshot(s.owned)
		if _, err := removeItemCount(&next.Items, item, true); err != nil {
			return 0, nil, true, err
		}
		if err := s.commitOwned(ctx, next); err != nil {
			return 0, nil, true, err
		}
		s.owned = next
		return 23, nil, true, nil
	}
	return s.itemStorageMove(ctx, path, item)
}

func removeItemCount(items *[]Item, want Item, unlocked bool) (Item, error) {
	for i, current := range *items {
		if current.InvenIndex != want.InvenIndex {
			continue
		}
		if current.ID != want.ID || current.Type != want.Type || current.Count < want.Count || (unlocked && current.KeepFlag != 0) {
			return Item{}, errors.New("inventory: item identity, count or lock mismatch")
		}
		moved := current
		moved.Count = want.Count
		if current.Count == want.Count {
			*items = slices.Delete(*items, i, i+1)
		} else {
			(*items)[i].Count -= want.Count
		}
		return moved, nil
	}
	return Item{}, errors.New("inventory: item is not in the requested inventory")
}

func (s *Inventory) itemLock(ctx command.Context, request []byte) (int, []byte, bool, error) {
	next := cloneOwnedSnapshot(s.owned)
	seen := map[uint64]bool{}
	err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		if field.Type != 2 {
			return errors.New("inventory: invalid lock entry")
		}
		var index, flag uint64
		if err := operationVarints(field.Value, map[int]*uint64{1: &index, 2: &flag}); err != nil {
			return err
		}
		if index == 0 || flag > 1 || seen[index] {
			return errors.New("inventory: invalid or duplicate lock index")
		}
		seen[index] = true
		for i, item := range next.Items {
			if item.InvenIndex == index {
				next.Items[i].KeepFlag = flag
				return nil
			}
		}
		return errors.New("inventory: lock item not in backpack")
	})
	if err != nil {
		return 0, nil, true, err
	}
	if len(seen) == 0 {
		return 0, nil, true, errors.New("inventory: empty lock request")
	}
	if err := s.commitOwned(ctx, next); err != nil {
		return 0, nil, true, err
	}
	s.owned = next
	return 233, nil, true, nil
}

func inventorySlotsUsed(items []Item) uint64 {
	var count uint64
	for _, item := range items {
		if item.Type != 13 && item.Type != 17 && item.Type != 46 && item.Type != 63 {
			count++
		}
	}
	return count
}

func (s *Inventory) itemStorageMove(ctx command.Context, path string, want Item) (int, []byte, bool, error) {
	if s.capacity == nil || s.itemStacks == nil {
		return 0, nil, true, errors.New("inventory: storage dependencies unavailable")
	}
	if _, exists := s.itemStacks.Limits[[2]uint64{want.Type, want.ID}]; !exists {
		return 0, nil, true, errors.New("inventory: item is not storable")
	}
	counts, err := s.capacity.InventorySlotCounts(ctx)
	if err != nil {
		return 0, nil, true, err
	}
	next := cloneOwnedSnapshot(s.owned)
	source, target := &next.Items, &next.Storage
	code, capacity := 76, counts.Storage
	if path == "/ItemStorageOut" {
		source, target = &next.Storage, &next.Items
		code, capacity = 77, counts.Items
	}
	moved, err := removeItemCount(source, want, false)
	if err != nil {
		return 0, nil, true, err
	}
	// The destination has its own stack identity. Replies are count deltas,
	// consumed by CommonPacket.AddKeepItem and AddItemInfo.
	destination := ownedSnapshot{Items: *target}
	deltas, err := s.addItems(ctx, &destination, moved)
	if err != nil {
		return 0, nil, true, err
	}
	used := inventorySlotsUsed(destination.Items)
	if code == 77 {
		used += inventorySlotsUsed(s.starter)
	}
	if used > capacity && inventorySlotsUsed(destination.Items) > inventorySlotsUsed(*target) {
		return 0, nil, true, errors.New("inventory: destination capacity exceeded")
	}
	*target = destination.Items
	if err := s.commitOwned(ctx, next); err != nil {
		return 0, nil, true, err
	}
	s.owned = next
	var response []byte
	for _, item := range deltas {
		response = wire.AppendBytes(response, 1, ItemWire(item))
	}
	return code, response, true, nil
}

func operationVarints(data []byte, fields map[int]*uint64) error {
	seen := map[int]bool{}
	return wire.Walk(data, func(field wire.Field) error {
		target, known := fields[field.Number]
		if !known {
			return nil
		}
		if field.Type != 0 || seen[field.Number] {
			return errors.New("inventory: malformed or duplicate operation field")
		}
		value, n := binary.Uvarint(field.Value)
		if n <= 0 {
			return errors.New("inventory: invalid operation scalar")
		}
		seen[field.Number] = true
		*target = value
		return nil
	})
}
