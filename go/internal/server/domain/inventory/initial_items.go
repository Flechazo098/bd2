package inventory

import (
	"fmt"

	"bd2server/internal/server/domain/command"
)

func (s *Inventory) InitializeItems(ctx command.Context, items []Item) error {
	if s.corePresent || len(s.owned.Items) != 0 {
		return fmt.Errorf("inventory: initial items require a new account")
	}
	next := cloneOwnedSnapshot(s.owned)
	seen := make(map[uint64]bool, len(s.starter)+len(items))
	for _, item := range s.starter {
		if item.InvenIndex != 0 {
			seen[item.InvenIndex] = true
		}
	}
	for _, item := range items {
		if item.InvenIndex == 0 || item.InvenIndex >= 1<<59 || item.Type == 0 || item.ID == 0 || item.Count == 0 || seen[item.InvenIndex] {
			return fmt.Errorf("inventory: invalid initial owned item")
		}
		seen[item.InvenIndex] = true
		next.Items = append(next.Items, item)
	}
	if err := s.commitOwned(ctx, next); err != nil {
		return err
	}
	s.owned = next
	return nil
}
