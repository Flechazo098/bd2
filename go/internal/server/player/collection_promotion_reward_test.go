package player

import (
	"path/filepath"
	"reflect"
	"testing"

	"bd2server/internal/server/gamedata"
)

func TestNewCostumeReusesPromotedCharacter(t *testing.T) {
	for _, inBase := range []bool{false, true} {
		t.Run(map[bool]string{false: "collection", true: "base"}[inBase], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "collection.json")
			store, err := OpenCollectionStore(testStore(path), nil)
			if err != nil {
				t.Fatal(err)
			}
			owned := Character{InvenIndex: 77, ID: 888, HP: 4000, Level: 100, CostumeID: 76541, UseCostume: 123, ConnectPotentialCostume: 76541, TalentLevel: 4}
			if inBase {
				if err := store.BindBaseCharacters([]Character{owned}); err != nil {
					t.Fatal(err)
				}
			} else {
				next := cloneCollection(store.data)
				next.Characters = []Character{owned}
				if err := store.commit(next); err != nil {
					t.Fatal(err)
				}
			}
			catalog, err := gamedata.NewRegularGachaCatalog(
				map[uint64]gamedata.RegularGacha{1: {ID: 1, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 76543, Weight: 1}}}},
				map[uint64]gamedata.CharacterDesign{76543: {ID: 999, GrowthCharacterIDs: []uint64{999, 888}, HP: 100, CostumeMaxLevel: 5, OverflowItemType: 20, OverflowItemCount: 2}},
			)
			if err != nil {
				t.Fatal(err)
			}
			before := store.data.NextCharacterIndex
			grant, err := store.GrantCostumes("new-costume", []uint64{76543}, catalog)
			if err != nil {
				t.Fatal(err)
			}
			if len(grant.CharacterIndices) != 0 || len(grant.CostumeIndices) != 1 || store.data.NextCharacterIndex != before {
				t.Fatalf("new character created after promotion: %+v", grant)
			}
			costume, ok := store.CostumeByIndex(grant.CostumeIndices[0])
			if !ok || costume.UseChar != owned.InvenIndex {
				t.Fatalf("costume=%+v", costume)
			}
			loaded, err := OpenCollectionStore(testStore(path), nil)
			if err != nil {
				t.Fatal(err)
			}
			if inBase {
				if err := loaded.BindBaseCharacters([]Character{owned}); err != nil {
					t.Fatal(err)
				}
			} else if got, ok := loaded.FindCharacter(owned.InvenIndex); !ok || !reflect.DeepEqual(got, owned) {
				t.Fatalf("original character changed: %+v", got)
			}
			retry, err := loaded.GrantCostumes("new-costume", []uint64{76543}, catalog)
			if err != nil || !reflect.DeepEqual(retry, grant) || len(loaded.Costumes()) != 1 {
				t.Fatalf("retry=%+v err=%v", retry, err)
			}
		})
	}
}

func TestCostumeRejectsAmbiguousPromotedOwnership(t *testing.T) {
	_, _, err := findCharacterByDesign(nil, []Character{{InvenIndex: 77, ID: 999}, {InvenIndex: 88, ID: 888}}, gamedata.CharacterDesign{ID: 999, GrowthCharacterIDs: []uint64{999, 888}})
	if err == nil {
		t.Fatal("duplicate family silently selected")
	}
}
