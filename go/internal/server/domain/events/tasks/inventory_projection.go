package eventtasks

import (
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"fmt"
)

type InventoryEquipment struct{ Level uint64 }
type InventoryCostume struct{ ID, Level uint64 }
type InventorySnapshot struct {
	Items     map[[2]uint64]uint64
	Equipment map[uint64]InventoryEquipment
	Costumes  map[uint64]InventoryCostume
}
type InventoryProvider interface {
	InventorySnapshot(ctx command.Context) (InventorySnapshot, error)
}
type ItemSource interface {
	All(ctx command.Context) []assets.Item
}
type EquipmentSource interface {
	All(ctx command.Context) []assets.Equipment
}
type CostumeSource interface{ Costumes() []roster.Costume }
type InventoryProjection struct {
	Items        ItemSource
	Equipment    EquipmentSource
	Costumes     CostumeSource
	StateVersion func() uint64
}

func (p *InventoryProjection) ObservationVersion() string {
	if p.StateVersion == nil {
		return ""
	}
	var costumes uint64
	if source, ok := p.Costumes.(interface{ ObservationVersion() uint64 }); ok {
		costumes = source.ObservationVersion()
	}
	return fmt.Sprintf("%d/%d", p.StateVersion(), costumes)
}

func (p *InventoryProjection) InventorySnapshot(ctx command.Context) (InventorySnapshot, error) {
	s := InventorySnapshot{Items: map[[2]uint64]uint64{}, Equipment: map[uint64]InventoryEquipment{}, Costumes: map[uint64]InventoryCostume{}}
	if p.Items != nil {
		for _, v := range p.Items.All(ctx) {
			s.Items[[2]uint64{v.Type, v.ID}] += v.Count
		}
	}
	if p.Equipment != nil {
		for _, v := range p.Equipment.All(ctx) {
			s.Equipment[v.InvenIndex] = InventoryEquipment{Level: v.Level}
		}
	}
	if p.Costumes != nil {
		for _, v := range p.Costumes.Costumes() {
			s.Costumes[v.InvenIndex] = InventoryCostume{ID: v.ID, Level: v.Level}
		}
	}
	return s, nil
}
