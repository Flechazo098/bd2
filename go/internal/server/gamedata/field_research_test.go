package gamedata

import (
	"os"
	"testing"
)

func TestInstalledFieldResearch23510(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	d, err := LoadFieldResearch(root, "20260923193640", 1)
	if err != nil {
		t.Fatal(err)
	}
	o := d.Objects[401]
	if o.CollectionID != 103 || o.Type != 1 || o.Reward.Type != 4 || o.Reward.Count != 345 || len(o.Maps) != 1 || o.Maps[0] != 4 {
		t.Fatalf("research table mismatch: %+v", o)
	}
	chars, err := LoadResearchCharacters(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	if len(chars) == 0 {
		t.Fatal("research talents empty")
	}
}
