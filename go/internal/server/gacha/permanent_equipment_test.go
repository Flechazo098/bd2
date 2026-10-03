package gacha

import (
	"bytes"
	"fmt"
	"os"
	"testing"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func TestPermanentEquipmentBuyUsesActiveCatalogWithoutSchedule(t *testing.T) {
	root := os.Getenv("BD2_TEST_GAMEDATA_ROOT")
	if root == "" {
		t.Skip("set BD2_TEST_GAMEDATA_ROOT for installed GameData integration test")
	}
	regular, catalog, err := gamedata.LoadActiveGachaForSchedules(root, "20260923193640", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, ticketCount := range []uint64{5, 10} {
		t.Run(fmt.Sprintf("tickets_%d", ticketCount), func(t *testing.T) {
			storage := stateio.NewMemory()
			service := newMultiBuyTestService(t, storage)
			service.regular = regular
			service.schedule.Schedules = nil
			equipment, err := player.OpenEquipmentInventory(storage)
			if err != nil {
				t.Fatal(err)
			}
			service.AttachEquipmentGacha(catalog, equipment)
			inventory, err := player.OpenInventory(storage, &player.Starter{Version: "2.35.10"})
			if err != nil {
				t.Fatal(err)
			}
			items, err := inventory.GrantOnce("tickets", []gamedata.BattleReward{{Type: 8, ID: 1000, Count: ticketCount + 1}})
			if err != nil {
				t.Fatal(err)
			}
			service.AttachInventory(inventory)
			ticket := items[0]
			ticket.Count = ticketCount
			request := wire.AppendVarint(nil, 1, 90)
			request = wire.AppendVarint(request, 2, 201)
			request = wire.AppendVarint(request, 3, 1)
			request = wire.AppendBytes(request, 4, player.ItemWire(ticket))
			code, response, handled, err := service.Handle("/GachaBuy", request)
			if err != nil || !handled || code != 146 {
				t.Fatalf("code=%d handled=%v err=%v", code, handled, err)
			}
			bundle, _, _ := wire.Bytes(response, 1)
			if countFields(bundle, 4) != 10 || len(equipment.All()) != 10 {
				t.Fatal("ten equipment instances missing")
			}
			if got := service.wallet.Snapshot().FreeJewelry; got != 1000-(10-ticketCount)*200 {
				t.Fatalf("free diamonds=%d", got)
			}
			user := service.collection.GachaUser(10002)
			if user.Point != 10 || user.TotalBuyCount != 10 {
				t.Fatalf("user=%+v", user)
			}
			if countFields(response, 3) != 4 {
				t.Fatal("shared pity snapshot missing")
			}
			for _, item := range inventory.All() {
				if item.InvenIndex == ticket.InvenIndex && item.Count != 1 {
					t.Fatalf("remaining tickets=%d", item.Count)
				}
			}
			_, replay, _, err := service.Handle("/GachaBuy", request)
			if err != nil || !bytes.Equal(response, replay) || len(equipment.All()) != 10 {
				t.Fatalf("retry changed draw: %v", err)
			}
		})
	}
}
