package inventory

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/ownership"
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
)

func ItemWire(v Item) []byte {
	return ownership.EncodeItem(ownership.Item{InvenIndex: v.InvenIndex, ID: v.ID, Type: v.Type, Count: v.Count, KeepFlag: v.KeepFlag, TimeValue: v.TimeValue, ExpiryTime: v.ExpiryTime, SortID: v.SortID, UseCount: v.UseCount})
}

func (s *RecipeService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path != "/RecipeInfo" {
		return 0, nil, false, nil
	}
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return 0, nil, true, errInvalidRecipeRequest
	}
	known := map[uint64]bool{}
	for _, id := range s.initial {
		known[id] = true
	}
	for _, item := range s.items.All(ctx) {
		// EElementType.CookingRecipe is protocol value 7. Ownership comes from
		// seed state or real reward grants, never from every row in CookingTable.
		if item.Type != 7 || item.Count == 0 {
			continue
		}
		if !s.design.IDs[item.ID] {
			return 0, nil, true, fmt.Errorf("recipes: owned recipe %d is absent from GameData", item.ID)
		}
		known[item.ID] = true
	}
	ids := make([]uint64, 0, len(known))
	for id := range known {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var packed []byte
	for _, id := range ids {
		packed = binary.AppendUvarint(packed, id)
	}
	if len(packed) == 0 {
		return 46, nil, true, nil
	}
	return 46, wire.AppendBytes(nil, 2, packed), true, nil
}

func (s *Inventory) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path != "/ItemInfo" && path != "/UseRandomBox" {
		return 0, nil, false, nil
	}
	if path == "/UseRandomBox" {
		return s.useRandomBox(ctx, request)
	}

	items := make([]Item, 0, len(s.starter)+len(s.owned.Items))
	items = append(items, s.starter...)
	items = append(items, s.owned.Items...)
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 {
		return 0, nil, true, fmt.Errorf("player: %s invalid sequence", path)
	}
	var response []byte
	for _, item := range items {
		response = wire.AppendBytes(response, 1, ItemWire(item))
	}
	return 21, response, true, nil
}

func decodeVarints(proto []byte, fields map[int]*uint64) error {
	return wire.Walk(proto, func(field wire.Field) error {
		value, known := fields[field.Number]
		if !known {
			return fmt.Errorf("player: unsupported starter field %d", field.Number)
		}
		if field.Type != 0 {
			return fmt.Errorf("player: starter field %d wire type %d", field.Number, field.Type)
		}
		*value, _ = binary.Uvarint(field.Value)
		return nil
	})
}

func (s *Inventory) useRandomBox(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 {
		return 0, nil, true, errors.New("player: UseRandomBox invalid sequence")
	}
	index, present, err := wire.Varint(request, 2)
	if err != nil || !present || index == 0 {
		return 0, nil, true, errors.New("player: UseRandomBox invalid inventory index")
	}
	count, present, err := wire.Varint(request, 3)
	if err != nil || !present || count == 0 || count > uint64(^uint32(0)>>1) {
		return 0, nil, true, errors.New("player: UseRandomBox invalid use count")
	}

	if s.randomBoxes == nil {
		return 0, nil, true, errors.New("player: UseRandomBox design unavailable")
	}
	boxAt := -1
	for i, item := range s.owned.Items {
		if item.InvenIndex == index {
			boxAt = i
			break
		}
	}
	if boxAt < 0 {
		return 0, nil, true, fmt.Errorf("player: UseRandomBox unknown inventory index %d", index)
	}
	box := s.owned.Items[boxAt]
	if box.Type != 9 || box.Count < count {
		return 0, nil, true, errors.New("player: UseRandomBox item or count mismatch")
	}
	rewards, err := s.randomBoxes.Open(box.ID, count)
	if err != nil {
		return 0, nil, true, err
	}
	next := cloneOwnedSnapshot(s.owned)
	if box.Count == count {
		next.Items = append(next.Items[:boxAt], next.Items[boxAt+1:]...)
	} else {
		next.Items[boxAt].Count -= count
	}
	granted := make([]Item, 0, len(rewards))
	for _, reward := range rewards {
		if reward.Type == 0 || reward.ID == 0 || reward.Count == 0 {
			return 0, nil, true, errors.New("player: UseRandomBox invalid GameData reward")
		}
		items, err := s.addItems(&next, Item{ID: reward.ID, Type: reward.Type, Count: reward.Count, TimeValue: uint64(time.Now().UnixMilli())})
		if err != nil {
			return 0, nil, true, err
		}
		granted = append(granted, items...)
	}
	if err := s.commitOwned(ctx, next); err != nil {
		return 0, nil, true, err
	}
	s.owned = next
	var bundle []byte
	for _, item := range mergeRewardDeltas(granted) {
		bundle = wire.AppendBytes(bundle, 1, ItemWire(item))
	}
	return 143, wire.AppendBytes(nil, 1, bundle), true, nil
}

func DecodeItemRequest(request []byte, number int, operation string) ([]Item, error) {
	var result []Item
	err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != number {
			return nil
		}
		if field.Type != 2 {
			return fmt.Errorf("player: %s invalid material", operation)
		}
		var item Item
		if err := decodeVarints(field.Value, map[int]*uint64{1: &item.InvenIndex, 2: &item.ID, 3: &item.Type, 4: &item.Count, 5: &item.KeepFlag, 6: &item.TimeValue, 8: &item.ExpiryTime, 9: &item.SortID, 10: &item.UseCount}); err != nil {
			return err
		}
		if item.Type == 0 || item.Count == 0 || (item.Type == 4 && (item.ID != 0 || item.InvenIndex != 0)) || (item.Type != 4 && (item.ID == 0 || item.InvenIndex == 0)) {
			return fmt.Errorf("player: %s invalid material", operation)
		}
		result = append(result, item)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("player: %s has no material", operation)
	}
	return result, nil
}
