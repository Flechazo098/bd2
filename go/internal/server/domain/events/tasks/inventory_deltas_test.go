package eventtasks

import (
	"testing"

	"bd2server/internal/server/domain/progression/conditions"
)

func TestInventoryDeltas(t *testing.T) {
	service := &Service{before: InventorySnapshot{
		Items:     map[[2]uint64]uint64{{8, 5}: 10, {9, 7}: 5},
		Equipment: map[uint64]InventoryEquipment{1: {Level: 2}},
		Costumes:  map[uint64]InventoryCostume{3: {ID: 7, Level: 1}},
	}}
	after := InventorySnapshot{
		Items:     map[[2]uint64]uint64{{8, 5}: 4, {9, 6}: 2, {9, 7}: 3},
		Equipment: map[uint64]InventoryEquipment{1: {Level: 3}},
		Costumes:  map[uint64]InventoryCostume{3: {ID: 7, Level: 2}},
	}
	got := map[conditions.Event]bool{}
	for _, event := range service.inventoryDeltas(after) {
		got[event] = true
	}
	want := []conditions.Event{
		{Type: conditions.ResourceUse, Sub: 5, Count: 6},
		{Type: conditions.ItemCurrencyGet, Sub: 9, Count: 2},
		{Type: conditions.EquipmentUpgrade, Count: 1},
		{Type: conditions.CostumeUpgrade, Sub: 7, Count: 1},
	}
	for _, event := range want {
		if !got[event] {
			t.Fatalf("missing event %+v in %v", event, got)
		}
	}
	nonResource := conditions.Event{Type: conditions.ResourceUse, Sub: 7, Count: 2}
	if got[nonResource] {
		t.Fatalf("non-resource spend reported as McResourceUse: %v", got)
	}
	if len(got) != len(want) {
		t.Fatalf("inventory deltas = %v, want %v", got, want)
	}
}
