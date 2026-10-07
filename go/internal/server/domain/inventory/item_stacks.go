package inventory

import (
	"bd2server/internal/server/design/gamedata"
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

// Reward ItemDBInfo.Count is a delta: CommonPacket.AddItemInfo adds it to an
// existing instance. Inventory state holds totals; replies hold only this grant.
func (s *Inventory) addItems(next *ownedSnapshot, reward Item) ([]Item, error) {
	if reward.ID == 0 || reward.Type == 0 || reward.Count == 0 || reward.Count > math.MaxInt32 {
		return nil, errors.New("inventory: invalid item reward")
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
		if next.NextIndex == math.MaxUint64 {
			return nil, errors.New("inventory: item index exhausted")
		}
		item := reward
		item.InvenIndex = next.NextIndex
		item.Count = min(remaining, limit)
		next.NextIndex++
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
