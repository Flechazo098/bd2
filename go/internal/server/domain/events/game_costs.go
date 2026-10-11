package events

import (
	"fmt"
	"math"

	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
)

func (e *Economy) AvailableGameItemCount(ctx command.Context, itemType, id uint64) (uint64, error) {
	if !inventoryType(itemType) || id == 0 {
		return 0, fmt.Errorf("events: unsupported game inventory cost %d/%d", itemType, id)
	}
	var total uint64
	for _, item := range e.items.All(ctx) {
		if item.Type != itemType || item.ID != id || item.Count == 0 {
			continue
		}
		if err := e.items.CanConsume(ctx, []assets.Item{item}); err != nil {
			continue
		}
		if item.Count > math.MaxUint64-total {
			return 0, fmt.Errorf("events: game item balance overflow %d/%d", itemType, id)
		}
		total += item.Count
	}
	return total, nil
}
