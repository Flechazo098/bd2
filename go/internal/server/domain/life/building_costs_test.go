package life_test

import (
	"testing"

	"bd2server/internal/server/protocol/wire"
)

// Farmland (LifeBuildingObjectTable 30008) lists three required items but four
// counts, the last one 0. The client walks the type list, so the extra count
// is ignored and placing it spends the three listed items.
func TestBuildingCostIgnoresCountsPastTheTypeList(t *testing.T) {
	g := scenario(t)
	g.d.Tables["LifeObjectGroupTable"][[3]uint64{304}] = proto(5, 304, 12, 3, 7, 1)
	g.d.Tables["LifeBuildingObjectTable"][[3]uint64{304}] = append(proto(4, 304, 11, 5, 8, 3), wire.AppendVarint(proto(9, 501, 10, 66), 8, 0)...)
	g.call("/LifeWorldChoice", proto(2, 91))
	g.fund("farmland", 0, 8)
	g.call("/LifeWorldObjectPlaceSave", child(nil, 2, placement(304, 5, 6)))
	if wood, err := g.s.Balance(g.ctx, 66, 501); err != nil || wood != 5 {
		t.Fatalf("wood after placing farmland = %d, want 5 (%v)", wood, err)
	}
}
