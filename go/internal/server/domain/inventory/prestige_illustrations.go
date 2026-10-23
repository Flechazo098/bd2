package inventory

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"bd2server/internal/server/domain/command"
)

func (s *Inventory) AttachPrestigeIllustrations(ctx command.Context, illustrations map[uint64]uint64) error {
	s.prestigeIllustrations = illustrations
	next := cloneOwnedSnapshot(s.owned)
	if err := s.ensurePrestigeIllustrations(ctx, &next, s.All(ctx)); err != nil {
		return err
	}
	if maps.Equal(next.PrestigeIllustrations, s.owned.PrestigeIllustrations) {
		return nil
	}
	if err := s.commitOwned(ctx, next); err != nil {
		return err
	}
	s.owned = next
	return nil
}

func (s *Inventory) loadPrestigeIllustrations(ctx command.Context) error {
	rows, err := s.store.ListEntries(ctx.State, "items", "prestige_illustrations")
	if err != nil {
		return err
	}
	used := make(map[uint64]bool, len(rows))
	for key, raw := range rows {
		id, err := strconv.ParseUint(key, 10, 64)
		var index uint64
		if err != nil || id == 0 || key != strconv.FormatUint(id, 10) || json.Unmarshal(raw, &index) != nil || index == 0 || index >= 1<<59 || used[index] {
			return fmt.Errorf("inventory: invalid prestige illustration identity %q", key)
		}
		s.owned.PrestigeIllustrations[id] = index
		used[index] = true
	}
	return nil
}

func (s *Inventory) ensurePrestigeIllustrations(ctx command.Context, next *ownedSnapshot, items []Item) error {
	if next.PrestigeIllustrations == nil {
		next.PrestigeIllustrations = make(map[uint64]uint64)
	}
	var missing []uint64
	for _, item := range items {
		if item.Type != 45 || item.Count == 0 {
			continue
		}
		if id := s.prestigeIllustrations[item.ID]; id != 0 && next.PrestigeIllustrations[id] == 0 {
			missing = append(missing, id)
		}
	}
	slices.Sort(missing)
	for _, id := range slices.Compact(missing) {
		index, err := ctx.NewInstanceID()
		if err != nil {
			return err
		}
		next.PrestigeIllustrations[id] = index
	}
	return nil
}

func (s *Inventory) withPrestigeIllustrations(items []Item) []Item {
	if len(s.prestigeIllustrations) == 0 {
		return items
	}
	seen := make(map[uint64]bool)
	for _, item := range items {
		if item.Type == 25 && item.Count != 0 {
			seen[item.ID] = true
		}
	}
	var attached []Item
	for _, item := range items {
		if item.Type != 45 || item.Count == 0 {
			continue
		}
		id := s.prestigeIllustrations[item.ID]
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		attached = append(attached, Item{InvenIndex: s.owned.PrestigeIllustrations[id], ID: id, Type: 25, Count: 1, TimeValue: item.TimeValue})
	}
	return append(items, attached...)
}
