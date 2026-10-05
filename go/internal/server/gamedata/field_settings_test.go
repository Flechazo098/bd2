package gamedata

import (
	"os"
	"testing"
	"time"
)

func TestInstalledFieldSettingsCapacityAndDispatchReset(t *testing.T) {
	root := "../../../../data/resources/GameData"
	if _, e := os.Stat(root); e != nil {
		t.Skip("installed data missing")
	}
	s, e := LoadFieldSettingsDesign(root, "20260923193640")
	if e != nil {
		t.Fatal(e)
	}
	if s.TalentSlots != 5 || len(s.CharacterTalentClass) == 0 || len(s.CharacterTemporaryPack) == 0 {
		t.Fatalf("unexpected field settings design %+v", s)
	}
	shops, e := LoadNPCShopDesign(root, "20260923193640")
	if e != nil {
		t.Fatal(e)
	}
	if len(shops.ShopNPCs[1]) != 1 || shops.ShopNPCs[1][0] != 4 {
		t.Fatal("pack1 interaction-3 binding changed", shops.ShopNPCs[1])
	}
	d, e := LoadTalentDispatchDesign(root, "20260923193640")
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range d {
		if row.Seconds != 79200 {
			t.Fatal("changed dispatch slider reference", row.ID, row.Seconds)
		}
		for _, now := range []time.Time{time.Date(2026, 10, 5, 23, 59, 59, 0, time.UTC), time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)} {
			end := row.EndTime(now)
			if !end.After(now) || end.Hour() != 0 || end.Minute() != 0 || end.Second() != 0 {
				t.Fatalf("invalid reset deadline %s %s", now, end)
			}
		}
	}
}
