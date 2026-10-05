package commerce

import (
	"fmt"
	"sort"

	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
)

// AttachEventShopSchedules joins event type 15 to EventShopTable and then to
// CashProductTable. Hub IDs are presentation identities, never product groups.
// All scheduled goods are published; purchase handlers enforce their windows.
// Call once before serving sessions.
func (s *Service) AttachEventShopSchedules(design *gamedata.CashCatalog, schedules []events.Schedule) error {
	if design == nil || s.shopWindows == nil {
		return fmt.Errorf("commerce: missing event shop catalog or shop seed")
	}
	shops := map[uint64]uint64{}
	groups := map[uint64]bool{}
	for _, shop := range design.EventShops {
		if shop.ID == 0 || shop.ProductGroupID == 0 || shops[shop.ID] != 0 || groups[shop.ProductGroupID] {
			return fmt.Errorf("commerce: invalid or duplicate event shop identity")
		}
		shops[shop.ID], groups[shop.ProductGroupID] = shop.ProductGroupID, true
	}
	windows := map[gamedata.CashProductKey][][2]uint64{}
	var products [][]byte
	for _, raw := range s.shopProducts {
		group, _, _ := wire.Varint(raw, 1)
		if !groups[group] {
			products = append(products, raw)
		}
	}
	rows := append([]events.Schedule(nil), schedules...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].UID < rows[j].UID })
	for _, row := range rows {
		if row.Type != 15 {
			continue
		}
		group := shops[row.ID]
		if group == 0 || row.UID == 0 || row.Start <= 0 || row.Start >= row.End {
			return fmt.Errorf("commerce: invalid event shop schedule %d", row.UID)
		}
		found := false
		start, end := uint64(row.Start), uint64(row.End)
		for _, product := range s.catalog.Designs() {
			key := product.Key
			if key.GroupID != group {
				continue
			}
			found = true
			for _, prior := range windows[key] {
				if start < prior[1] && prior[0] < end {
					return fmt.Errorf("commerce: overlapping event shop product windows")
				}
			}
			windows[key] = append(windows[key], [2]uint64{start, end})
			raw := wire.AppendVarint(nil, 1, key.GroupID)
			raw = wire.AppendVarint(raw, 2, key.ProductID)
			raw = wire.AppendVarint(raw, 3, key.SaleGroup)
			raw = wire.AppendVarint(raw, 4, start)
			raw = wire.AppendVarint(raw, 5, end)
			raw = wire.AppendVarint(raw, 8, row.UID)
			products = append(products, raw)
		}
		if !found {
			return fmt.Errorf("commerce: event shop %d has no products", row.ID)
		}
	}
	s.shopProducts, s.eventShopWindows, s.eventShopGroups = products, windows, groups
	return nil
}
