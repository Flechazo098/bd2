package feature

import (
	"encoding/binary"
	"reflect"
	"testing"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
)

func TestRecipesFollowOwnedGrantsAndRestart(t *testing.T) {
	store := stateio.NewMemory()
	starter := &player.Starter{Version: versionconfig.State()}
	items, err := player.OpenInventory(store, starter)
	if err != nil {
		t.Fatal(err)
	}
	design := &gamedata.CookingRecipeDesign{IDs: map[uint64]bool{501: true, 701: true}}
	service, err := NewRecipeService(design, []uint64{701}, items)
	if err != nil {
		t.Fatal(err)
	}
	check := func(service *RecipeService, want []uint64) {
		t.Helper()
		code, payload, handled, err := service.Handle("/RecipeInfo", wire.AppendVarint(nil, 1, 17))
		if err != nil || !handled || code != 46 {
			t.Fatalf("recipe response: %d %v %v", code, handled, err)
		}
		packed, _, err := wire.Bytes(payload, 2)
		var ids []uint64
		for len(packed) > 0 {
			id, n := binary.Uvarint(packed)
			if n <= 0 {
				t.Fatal("malformed packed recipes")
			}
			ids = append(ids, id)
			packed = packed[n:]
		}
		if err != nil || !reflect.DeepEqual(ids, want) {
			t.Fatalf("recipes=%v want=%v err=%v", ids, want, err)
		}
	}
	check(service, []uint64{701})
	if _, err := items.GrantOnce("recipe-reward", []gamedata.BattleReward{{Type: 7, ID: 501, Count: 1}, {Type: 7, ID: 701, Count: 1}, {Type: 1, ID: 999, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	check(service, []uint64{501, 701})
	reopened, err := player.OpenInventory(store, starter)
	if err != nil {
		t.Fatal(err)
	}
	service, err = NewRecipeService(design, []uint64{701}, reopened)
	if err != nil {
		t.Fatal(err)
	}
	check(service, []uint64{501, 701})
	if _, err := reopened.GrantOnce("unknown-recipe", []gamedata.BattleReward{{Type: 7, ID: 800, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Handle("/RecipeInfo", wire.AppendVarint(nil, 1, 18)); err == nil {
		t.Fatal("unknown owned recipe accepted")
	}
}

func TestRecipeDesignRejectsUnknownInitialOwnership(t *testing.T) {
	if _, err := NewRecipeService(&gamedata.CookingRecipeDesign{IDs: map[uint64]bool{501: true}}, []uint64{101}, statefulRecipeItems{}); err == nil {
		t.Fatal("unknown initial recipe accepted")
	}
}

type statefulRecipeItems struct{}

func (statefulRecipeItems) All() []player.Item { return nil }
