package player

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"testing"
)

func TestInventorySlotChargesDesignedCurrencyAndReplays(t *testing.T) {
	for _, typ := range []uint64{2, 3, 4} {
		t.Run(string(rune('0'+typ)), func(t *testing.T) {
			store := stateio.NewMemory()
			wallet, err := OpenWallet(store, Currency{Jewelry: 1000, FreeJewelry: 1000, Gold: 1000})
			if err != nil {
				t.Fatal(err)
			}
			rule := gamedata.InventorySlotRule{Default: 10, Maximum: 20, PriceType: typ, BasePrice: 30, MaxPrice: 100}
			s, err := OpenInventorySlots(store, &gamedata.InventorySlotDesign{Items: rule, Storage: rule, Equipment: rule, EquipmentStorage: rule}, InventorySlotCounts{10, 10, 10, 10}, wallet)
			if err != nil {
				t.Fatal(err)
			}
			s.BeginSession("test")
			req := wire.AppendVarint(wire.AppendVarint(nil, 1, 12), 2, 2)
			if _, _, _, err = s.Handle("/InvenAddSlot", req); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err = s.Handle("/InvenAddSlot", req); err != nil {
				t.Fatal(err)
			}
			got := wallet.Snapshot()
			want := Currency{Jewelry: 1000, FreeJewelry: 1000, Gold: 1000}
			switch typ {
			case 2:
				want.Jewelry -= 90
			case 3:
				want.FreeJewelry -= 90
			case 4:
				want.Gold -= 90
			}
			if got != want {
				t.Fatalf("currency=%+v want=%+v", got, want)
			}
			if s.state.Items != 12 {
				t.Fatal("replay expanded slots twice")
			}
		})
	}
}
