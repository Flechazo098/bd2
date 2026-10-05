package gamedata

import (
	"os"
	"testing"
)

func TestInstalledRewardEquipmentCatalog(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("explicit installed GameData integration environment required")
	}
	d, err := LoadRewardEquipmentCatalog(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.equipment) == 0 {
		t.Fatal("empty equipment catalog")
	}
	for id := range d.equipment {
		if _, _, _, err := d.RollOptions(id); err != nil {
			t.Fatalf("equipment %d: %v", id, err)
		}
	}
}
