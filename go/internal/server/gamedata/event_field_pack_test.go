package gamedata

import (
	"os"
	"reflect"
	"testing"
)

func TestInstalledHiddenTrackerPackRules(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	catalog, err := LoadEventPlayCatalog(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	// Current GameData PackTable 12010: hidden type 100, no price/reward
	// fields. MapTable binds all three maps, independently of minigame rows.
	pack, ok := catalog.FieldPacks[12010]
	if !ok || pack.BuyPrice != 0 || pack.BuyType != 0 || len(pack.BuyRewards) != 0 || !reflect.DeepEqual(pack.MapIDs, []int{1201001, 1201002, 1201003}) || pack.InitialPosition != "{}" {
		t.Fatalf("tracker pack %+v exists %v", pack, ok)
	}
	for _, game := range catalog.Tables["PackEventMiniGameTable"] {
		id, _ := optionalScalar(game, 12)
		if id == 12010 {
			t.Fatal("tracker unexpectedly became a PackEventMiniGameTable pack; recheck authoritative mapping")
		}
	}
}
