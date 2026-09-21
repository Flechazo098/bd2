package player

import (
	"path/filepath"
	"testing"

	"bd2server/internal/gamedata"
)

func TestEarnedQuestCostumeSharesPotentialLedgerAndKeepsSingleOwnedInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collection.json")
	store, err := OpenCollectionStore(path, []Costume{{InvenIndex: 123, ID: 60101, UseChar: 44}})
	if err != nil {
		t.Fatal(err)
	}
	reward := Costume{InvenIndex: 609338889, ID: 3501, UseChar: 535607162}
	if err := store.AttachRewardCostume(reward); err != nil {
		t.Fatal(err)
	}
	if got := store.Costumes(); len(got) != 2 || got[1].InvenIndex != reward.InvenIndex {
		t.Fatalf("earned costume view=%+v", got)
	}
	if err := store.ActivateCostumePotential(reward.InvenIndex, []uint64{1, 2}); err != nil {
		t.Fatal(err)
	}
	got, found := store.CostumeByIndex(reward.InvenIndex)
	if !found || len(got.PotentialIDs) != 2 || got.PotentialIDs[0] != 1 || got.PotentialIDs[1] != 2 {
		t.Fatalf("earned costume potential=%+v found=%v", got, found)
	}
	restarted, err := OpenCollectionStore(path, []Costume{{InvenIndex: 123, ID: 60101, UseChar: 44}})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.AttachRewardCostume(reward); err != nil {
		t.Fatal(err)
	}
	got, found = restarted.CostumeByIndex(reward.InvenIndex)
	if !found || len(got.PotentialIDs) != 2 {
		t.Fatalf("restarted earned costume potential=%+v found=%v", got, found)
	}
	if err := restarted.AttachRewardCostume(reward); err == nil {
		t.Fatal("duplicate earned quest costume accepted")
	}
}

func TestUpdateCollectionCharacterAcrossPromotionChangesDesignNotInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collection.json")
	store, err := OpenCollectionStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	store.data.Characters = []Character{{InvenIndex: 920000054, ID: 6510, Level: 1}}
	if err := store.commit(store.data); err != nil {
		t.Fatal(err)
	}
	promoted := Character{InvenIndex: 920000054, ID: 6514, Level: 100}
	if err := store.CanUpdateCharacter(6510, promoted); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateCharacter(6510, promoted); err != nil {
		t.Fatal(err)
	}
	loaded, err := OpenCollectionStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, found := loaded.FindCharacter(920000054); !found || got.ID != 6514 || got.Level != 100 {
		t.Fatalf("persisted promoted collection character=%+v found=%v", got, found)
	}
	if err := loaded.UpdateCharacter(6510, promoted); err == nil {
		t.Fatal("stale old design accepted")
	}
	if err := loaded.CanUpdateCharacter(6514, Character{InvenIndex: 920000055, ID: 6514, Level: 100}); err == nil {
		t.Fatal("unknown instance accepted")
	}
}

func TestRepairCostumeOverflowCapsLegacyLevelAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collection.json")
	store, err := OpenCollectionStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	store.data.Characters = []Character{{InvenIndex: 920000001, ID: 6490, Level: 1, UseCostume: 930000001, ConnectPotentialCostume: 64901}}
	store.data.Costumes = []Costume{{InvenIndex: 930000001, ID: 64901, Level: 7, UseChar: 920000001}}
	store.data.Grants["draw"] = CollectionGrant{
		Upgrades: []CostumeUpgrade{
			{InvenIndex: 930000001, CostumeID: 64901, Before: 4, After: 5},
			{InvenIndex: 930000001, CostumeID: 64901, Before: 5, After: 6, SortID: 1},
			{InvenIndex: 930000001, CostumeID: 64901, Before: 6, After: 7, SortID: 2},
		},
		ViewCostumeIDs: []uint64{64901, 64901, 64901},
	}
	if err := store.commit(store.data); err != nil {
		t.Fatal(err)
	}
	resolve := func(id uint64) (gamedata.CharacterDesign, bool) {
		if id != 64901 {
			return gamedata.CharacterDesign{}, false
		}
		return gamedata.CharacterDesign{ID: 6490, HP: 166, CostumeMaxLevel: 5, OverflowItemType: 20, OverflowItemCount: 2}, true
	}
	repaired, err := store.RepairCostumeOverflow(resolve)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Costumes()[0].Level; got != 5 {
		t.Fatalf("level=%d want=5", got)
	}
	grant, ok := store.Grant("draw")
	if !ok || len(grant.Upgrades) != 3 || len(grant.Exchanges) != 2 || len(repaired["draw"]) != 2 {
		t.Fatalf("grant=%+v repaired=%+v", grant, repaired)
	}
	for _, upgrade := range grant.Upgrades[1:] {
		if upgrade.Before != 5 || upgrade.After != 5 || upgrade.InvenIndex != 930000001 {
			t.Fatalf("overflow display upgrade=%+v", upgrade)
		}
	}
	if _, err := store.RepairCostumeOverflow(resolve); err != nil {
		t.Fatal(err)
	}
	grant, _ = store.Grant("draw")
	if len(grant.Exchanges) != 2 || len(grant.Upgrades) != 3 {
		t.Fatalf("second repair duplicated grant entries: %+v", grant)
	}
	restored, err := OpenCollectionStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.Costumes()[0].Level; got != 5 {
		t.Fatalf("restored level=%d want=5", got)
	}
}

func TestMaxCostumeDuplicatePersistsExchangeAndDisplayUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collection.json")
	store, err := OpenCollectionStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	store.data.Characters = []Character{{InvenIndex: 920000001, ID: 6490, Level: 1, UseCostume: 930000001, ConnectPotentialCostume: 64901}}
	store.data.Costumes = []Costume{{InvenIndex: 930000001, ID: 64901, Level: 5, UseChar: 920000001}}
	if err := store.commit(store.data); err != nil {
		t.Fatal(err)
	}
	catalog, err := gamedata.NewRegularGachaCatalog(
		map[uint64]gamedata.RegularGacha{1: {ID: 1, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 64901, Weight: 1}}}},
		map[uint64]gamedata.CharacterDesign{64901: {ID: 6490, HP: 166, CostumeMaxLevel: 5, OverflowItemType: 20, OverflowItemCount: 2}},
	)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := store.GrantRegular("draw", []uint64{64901}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(grant.Exchanges) != 1 || len(grant.Upgrades) != 1 {
		t.Fatalf("grant=%+v", grant)
	}
	if got := grant.Upgrades[0]; got.InvenIndex != 930000001 || got.CostumeID != 64901 || got.Before != 5 || got.After != 5 || got.SortID != 0 {
		t.Fatalf("display upgrade=%+v", got)
	}
	if got := store.Costumes()[0].Level; got != 5 {
		t.Fatalf("costume level=%d want=5", got)
	}
	restored, err := OpenCollectionStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := restored.Grant("draw")
	if !ok || len(persisted.Exchanges) != 1 || len(persisted.Upgrades) != 1 || restored.Costumes()[0].Level != 5 {
		t.Fatalf("persisted=%+v costumes=%+v", persisted, restored.Costumes())
	}
}

func TestGrantDifferentCostumesForSameCharacterReusesCharacter(t *testing.T) {
	store, err := OpenCollectionStore(filepath.Join(t.TempDir(), "collection.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := gamedata.NewRegularGachaCatalog(
		map[uint64]gamedata.RegularGacha{1: {ID: 1, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 64901, Weight: 1}}}},
		map[uint64]gamedata.CharacterDesign{
			64901: {ID: 6490, HP: 166, CostumeMaxLevel: 5},
			64902: {ID: 6490, HP: 166, CostumeMaxLevel: 5},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.GrantRegular("first", []uint64{64901}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.GrantRegular("second", []uint64{64902}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.CharacterIndices) != 1 || len(second.CharacterIndices) != 0 || len(store.Characters()) != 1 {
		t.Fatalf("first=%+v second=%+v characters=%+v", first, second, store.Characters())
	}
	costumes := store.Costumes()
	if len(costumes) != 2 || costumes[0].UseChar != costumes[1].UseChar || costumes[0].UseChar != store.Characters()[0].InvenIndex {
		t.Fatalf("costumes=%+v characters=%+v", costumes, store.Characters())
	}
	if store.Characters()[0].CostumeID != 64901 {
		t.Fatalf("initial character costume_id=%d", store.Characters()[0].CostumeID)
	}
}

func TestFirstGachaCompletionIsExplicitAndAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collection.json")
	store, err := OpenCollectionStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if store.FirstGachaCompleted() {
		t.Fatal("new collection reports completed first gacha")
	}
	catalog, err := gamedata.NewRegularGachaCatalog(
		map[uint64]gamedata.RegularGacha{20: {ID: 20, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 64901, Weight: 1}}}},
		map[uint64]gamedata.CharacterDesign{64901: {ID: 6490, HP: 166, CostumeMaxLevel: 5}},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.GrantRegularPurchase("regular-gacha:20:seq:1", []uint64{64901}, catalog, GachaPurchase{
		Group: gamedata.GachaGroupDesign{ID: 2, GachaSubType: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !store.FirstGachaCompleted() {
		t.Fatal("subtype-3 purchase did not persist first-gacha completion")
	}
	restored, err := OpenCollectionStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.FirstGachaCompleted() {
		t.Fatal("first-gacha completion did not survive restart")
	}
}

func TestFirstGachaCompletionRejectsRewardPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collection.json")
	store, err := OpenCollectionStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	store.data.Grants[FirstGachaCompletedIdentity] = CollectionGrant{ViewCostumeIDs: []uint64{1}}
	if err := store.commit(store.data); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCollectionStore(path, nil); err == nil {
		t.Fatal("collection accepted a non-empty first-gacha marker")
	}
}

func TestGrantCostumeForBaseCharacterDoesNotCreateCharacter(t *testing.T) {
	store, err := OpenCollectionStore(filepath.Join(t.TempDir(), "collection.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	base := []Character{{InvenIndex: 535604118, ID: 6490, Level: 1, UseCostume: 635604118}}
	if err := store.AttachBaseCharacters(base); err != nil {
		t.Fatal(err)
	}
	catalog, err := gamedata.NewRegularGachaCatalog(
		map[uint64]gamedata.RegularGacha{1: {ID: 1, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 64902, Weight: 1}}}},
		map[uint64]gamedata.CharacterDesign{64902: {ID: 6490, HP: 166, CostumeMaxLevel: 5}},
	)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := store.GrantRegular("base-costume", []uint64{64902}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(grant.CharacterIndices) != 0 || len(store.Characters()) != 0 || len(store.Costumes()) != 1 || store.Costumes()[0].UseChar != base[0].InvenIndex {
		t.Fatalf("grant=%+v characters=%+v costumes=%+v", grant, store.Characters(), store.Costumes())
	}
}

func TestAttachBaseCharactersRepairsDuplicatesAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collection.json")
	store, err := OpenCollectionStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	store.data.Characters = []Character{
		{InvenIndex: 920000001, ID: 6490, Level: 1, UseCostume: 930000001},
		{InvenIndex: 920000002, ID: 6490, Level: 1, UseCostume: 930000002},
		{InvenIndex: 920000003, ID: 50, Level: 1, UseCostume: 930000003},
		{InvenIndex: 920000004, ID: 50, Level: 1, UseCostume: 930000004},
	}
	store.data.Costumes = []Costume{
		{InvenIndex: 930000001, ID: 64902, Level: 2, UseChar: 920000001},
		{InvenIndex: 930000002, ID: 64903, Level: 4, UseChar: 920000002},
		{InvenIndex: 930000003, ID: 501, Level: 1, UseChar: 920000003},
		{InvenIndex: 930000004, ID: 502, Level: 3, UseChar: 920000004},
	}
	store.data.Grants["old"] = CollectionGrant{CharacterIndices: []uint64{920000001, 920000002, 920000003, 920000004}, CostumeIndices: []uint64{930000001, 930000002, 930000003, 930000004}}
	if err := store.commit(store.data); err != nil {
		t.Fatal(err)
	}
	base := []Character{{InvenIndex: 535604118, ID: 6490, Level: 1}}
	if err := store.AttachBaseCharacters(base); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachBaseCharacters(base); err != nil {
		t.Fatal(err)
	}
	characters := store.Characters()
	if len(characters) != 1 || characters[0].InvenIndex != 920000003 || characters[0].ID != 50 {
		t.Fatalf("characters=%+v", characters)
	}
	costumes := store.Costumes()
	wantUse := []uint64{535604118, 535604118, 920000003, 920000003}
	for i := range costumes {
		if costumes[i].UseChar != wantUse[i] {
			t.Fatalf("costume[%d]=%+v want use_char=%d", i, costumes[i], wantUse[i])
		}
	}
	grant, ok := store.Grant("old")
	if !ok || len(grant.CharacterIndices) != 1 || grant.CharacterIndices[0] != 920000003 || len(grant.CostumeIndices) != 4 {
		t.Fatalf("grant=%+v", grant)
	}
	if costumes[1].Level != 4 || costumes[3].Level != 3 {
		t.Fatalf("costume levels changed: %+v", costumes)
	}
}
