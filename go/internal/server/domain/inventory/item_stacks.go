package inventory

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"errors"
	"math"
)

func (s *Inventory) AttachItemStacks(design *gamedata.ItemStackDesign) error {
	if design == nil || len(design.Limits) == 0 {
		return errors.New("inventory: missing item stack design")
	}
	s.itemStacks = design
	return nil
}

// AttachStackOverride supplies the account's switch that lifts every stack limit.
func (s *Inventory) AttachStackOverride(unlimited func(command.Context) (bool, error)) {
	s.unlimitedStacks = unlimited
}

func (s *Inventory) stacksUnlimited(ctx command.Context) (bool, error) {
	if s.unlimitedStacks == nil {
		return false, nil
	}
	return s.unlimitedStacks(ctx)
}

// NormalizeItemStacks reshapes stacks to match the override and reports
// whether anything changed. While it is on, scattered stacks of one item merge
// into one. While it is off, stacks above their limit split back into
// limit-sized stacks as long as free slots remain; whatever does not fit is
// deleted. It is idempotent, so it may run before every request.
func (s *Inventory) NormalizeItemStacks(ctx command.Context) (bool, error) {
	if s.itemStacks == nil {
		return false, errors.New("inventory: item stack design not attached")
	}
	unlimited, err := s.stacksUnlimited(ctx)
	if err != nil {
		return false, err
	}
	next := cloneOwnedSnapshot(s.owned)
	var itemsChanged, storageChanged bool
	if unlimited {
		next.Items, itemsChanged = s.mergeStacks(next.Items)
		next.Storage, storageChanged = s.mergeStacks(next.Storage)
	} else {
		itemSlots, storageSlots := uint64(math.MaxUint64), uint64(math.MaxUint64)
		if s.capacity != nil {
			counts, err := s.capacity.InventorySlotCounts(ctx)
			if err != nil {
				return false, err
			}
			itemSlots, storageSlots = counts.Items-min(counts.Items, inventorySlotsUsed(s.starter)), counts.Storage
		}
		if next.Items, itemsChanged, err = s.splitStacks(ctx, next.Items, itemSlots); err != nil {
			return false, err
		}
		if next.Storage, storageChanged, err = s.splitStacks(ctx, next.Storage, storageSlots); err != nil {
			return false, err
		}
	}
	if !itemsChanged && !storageChanged {
		return false, nil
	}
	if err := s.commitOwned(ctx, next); err != nil {
		return false, err
	}
	s.owned = next
	return true, nil
}

// stackLimit is the GameData limit of an item that addItems merges.
func (s *Inventory) stackLimit(item Item) (uint64, bool) {
	if item.Type != 5 && item.Type != 8 && item.Type != 9 && item.Type != 14 || item.Pictorialbook != nil {
		return 0, false
	}
	limit, found := s.itemStacks.Limits[[2]uint64{item.Type, item.ID}]
	return limit, found && limit > 0
}

type stackKey struct{ typ, id, expiry, keep, uses uint64 }

func keyOfStack(item Item) stackKey {
	return stackKey{item.Type, item.ID, item.ExpiryTime, item.KeepFlag, item.UseCount}
}

// mergeStacks folds every stack into the first stack of the same item,
// keeping its instance; the absorbed instances are removed.
func (s *Inventory) mergeStacks(items []Item) ([]Item, bool) {
	first := map[stackKey]int{}
	out := make([]Item, 0, len(items))
	changed := false
	for _, item := range items {
		if _, ok := s.stackLimit(item); ok {
			if at, found := first[keyOfStack(item)]; found && out[at].Count <= math.MaxInt32-item.Count {
				out[at].Count += item.Count
				changed = true
				continue
			}
			first[keyOfStack(item)] = len(out)
		}
		out = append(out, item)
	}
	return out, changed
}

// splitStacks cuts stacks above their limit into limit-sized stacks while
// slots remain free and deletes the rest.
func (s *Inventory) splitStacks(ctx command.Context, items []Item, slots uint64) ([]Item, bool, error) {
	out := append([]Item(nil), items...)
	used := inventorySlotsUsed(out)
	changed := false
	for i := range items {
		limit, ok := s.stackLimit(out[i])
		if !ok || out[i].Count <= limit {
			continue
		}
		remaining := out[i].Count - limit
		out[i].Count = limit
		changed = true
		for remaining > 0 && used < slots {
			index, err := ctx.NewInstanceID()
			if err != nil {
				return nil, false, err
			}
			piece := out[i]
			piece.InvenIndex = index
			piece.Count = min(remaining, limit)
			out = append(out, piece)
			used++
			remaining -= piece.Count
		}
	}
	return out, changed, nil
}

// Reward ItemDBInfo.Count is a delta: CommonPacket.AddItemInfo adds it to an
// existing instance. Inventory state holds totals; replies hold only this grant.
func (s *Inventory) addItems(ctx command.Context, next *ownedSnapshot, reward Item) ([]Item, error) {
	if reward.ID == 0 || reward.Type == 0 || reward.Count == 0 || reward.Count > math.MaxInt32 {
		return nil, errors.New("inventory: invalid item reward")
	}
	if reward.Type == 45 {
		for _, items := range [][]Item{s.starter, next.Items} {
			for _, item := range items {
				if item.Type == 45 && item.ID == reward.ID && item.Count > 0 {
					return nil, nil
				}
			}
		}
		reward.Count = 1
	}
	if err := s.ensurePrestigeIllustrations(ctx, next, []Item{reward}); err != nil {
		return nil, err
	}
	limit := uint64(math.MaxInt32)
	stackable := reward.Type == 5 || reward.Type == 8 || reward.Type == 9 || reward.Type == 14
	if stackable {
		if s.itemStacks == nil {
			return nil, errors.New("inventory: item stack design not attached")
		}
		var found bool
		limit, found = s.itemStacks.Limits[[2]uint64{reward.Type, reward.ID}]
		if !found || limit == 0 || limit > math.MaxInt32 {
			return nil, errors.New("inventory: item stack limit unavailable")
		}
		unlimited, err := s.stacksUnlimited(ctx)
		if err != nil {
			return nil, err
		}
		if unlimited {
			limit = math.MaxInt32
		}
	}
	remaining := reward.Count
	var deltas []Item
	if stackable {
		for i, current := range next.Items {
			if current.Type != reward.Type || current.ID != reward.ID || current.ExpiryTime != reward.ExpiryTime ||
				current.KeepFlag != reward.KeepFlag || current.UseCount != reward.UseCount || current.Count >= limit || current.Pictorialbook != nil {
				continue
			}
			count := min(limit-current.Count, remaining)
			next.Items[i].Count += count
			delta := current
			delta.Count = count
			delta.SortID = reward.SortID
			deltas = append(deltas, delta)
			remaining -= count
			if remaining == 0 {
				return deltas, nil
			}
		}
	}
	for remaining > 0 {
		index, err := ctx.NewInstanceID()
		if err != nil {
			return nil, err
		}
		item := reward
		item.InvenIndex = index
		item.Count = min(remaining, limit)
		next.Items = append(next.Items, item)
		deltas = append(deltas, item)
		remaining -= item.Count
	}
	return deltas, nil
}

func mergeRewardDeltas(items []Item) []Item {
	indices := make(map[uint64]int, len(items))
	var result []Item
	for _, item := range items {
		if at, found := indices[item.InvenIndex]; found {
			result[at].Count += item.Count
		} else {
			indices[item.InvenIndex] = len(result)
			result = append(result, item)
		}
	}
	return result
}

func recordItemGrant(next *ownedSnapshot, identity string, items []Item) []Item {
	items = mergeRewardDeltas(items)
	next.GrantRewards[identity] = append([]Item(nil), items...)
	for _, item := range items {
		next.GrantItems[identity] = append(next.GrantItems[identity], item.InvenIndex)
	}
	return items
}
