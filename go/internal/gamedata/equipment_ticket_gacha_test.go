package gamedata

import (
	"os"
	"testing"
)

func TestEquipmentTicketOnlyGachaAgainstInstalledVersion23413(t *testing.T) {
	root := os.Getenv("BD2_TEST_GAMEDATA_ROOT")
	if root == "" {
		t.Skip("set BD2_TEST_GAMEDATA_ROOT for installed GameData integration test")
	}
	catalog, err := LoadEquipmentGacha(root, "20260910162539")
	if err != nil {
		t.Fatal(err)
	}
	gacha, ok := catalog.Gacha(71200001)
	if !ok || !gacha.TicketOnly || gacha.Count != 10 || gacha.Price != 0 || gacha.PriceType != 0 || len(gacha.TicketIDs) != 1 || gacha.TicketIDs[0] != 1104 {
		t.Fatalf("ticket-only equipment gacha=%+v ok=%v", gacha, ok)
	}
	if len(gacha.Pool) != 3 || gacha.Pool[0].Weight != 150 || gacha.Pool[1].Weight != 350 || gacha.Pool[2].Weight != 500 {
		t.Fatalf("ticket-only equipment pool=%+v", gacha.Pool)
	}
	if len(gacha.Pool[0].Children) != 50 || len(gacha.Pool[1].Children) != 9 || len(gacha.Pool[2].Children) != 14 {
		t.Fatalf("ticket-only equipment branches=%d/%d/%d", len(gacha.Pool[0].Children), len(gacha.Pool[1].Children), len(gacha.Pool[2].Children))
	}
	// These branches separate the owning character's star grade, not the
	// equipment rarity. Every candidate is EquipmentTable.Grade=4 (UR).
	for _, branch := range gacha.Pool {
		for _, item := range branch.Children {
			design, found := catalog.equipment[item.ID]
			if !found || design.Grade != 4 {
				t.Fatalf("UR-guaranteed candidate %d design=%+v found=%v", item.ID, design, found)
			}
		}
	}
	if _, grouped := catalog.GroupForGacha(gacha.ID); grouped {
		t.Fatal("standalone ticket draw incorrectly attached to a schedule group")
	}
}
