package gacha

import (
	"bytes"
	"testing"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func TestFirstGachaPreviewConfirmAndReplay(t *testing.T) {
	characters := make(map[uint64]gamedata.CharacterDesign)
	equipmentDesign := make(map[uint64]gamedata.EquipmentDesign)
	program := &gamedata.FirstGachaRewardGroup{ID: 20, DropCount: 1, DropType: 1}
	for i := uint64(0); i < 5; i++ {
		costumeID := 61001 + i
		equipmentID := 71001 + i
		characters[costumeID] = fixtureCharacter(6100+i, 100+i)
		equipmentDesign[equipmentID] = gamedata.EquipmentDesign{ID: equipmentID, Grade: 4}
		program.Entries = append(program.Entries,
			gamedata.FirstGachaRewardEntry{ItemType: 11, ItemID: costumeID, Count: 1, Weight: 1},
			gamedata.FirstGachaRewardEntry{ItemType: 10, ItemID: equipmentID, Count: 1, Weight: 1})
	}
	first, err := gamedata.NewFirstGachaDesign(gamedata.GachaGroupDesign{ID: 2, GachaSubType: 3, TenTimeGachaID: 20}, 20, 10, program, characters, equipmentDesign)
	if err != nil {
		t.Fatal(err)
	}
	infinite, err := gamedata.NewInfiniteGachaDesign(10, []uint64{61001}, characters)
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{
		1: {ID: 1, Count: 1, PriceType: 3, Price: 200, Pool: []gamedata.WeightedCostume{{ID: 61001, Weight: 1}}},
	}, characters)
	if err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	equipment, err := player.OpenEquipmentInventory(storage)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(infinite, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AttachFirstGacha(first); err != nil {
		t.Fatal(err)
	}
	service.AttachEquipmentGacha(first.EquipmentCatalog(), equipment)
	service.BeginSession("first-session")

	previewRequest := wire.AppendVarint(wire.AppendVarint(nil, 1, 11), 2, 20)
	code, preview, handled, err := service.Handle("/GachaBuyPreview", previewRequest)
	if err != nil || !handled || code != 175 {
		t.Fatalf("preview code=%d handled=%v err=%v", code, handled, err)
	}
	bundle, found, err := wire.Bytes(preview, 1)
	if err != nil || !found || countFields(bundle, 3) != 5 || countFields(bundle, 4) != 5 {
		t.Fatalf("preview costumes=%d equipment=%d found=%v err=%v", countFields(bundle, 3), countFields(bundle, 4), found, err)
	}
	if len(collection.Costumes()) != 0 || len(equipment.All()) != 0 || service.FirstGachaCompleted() {
		t.Fatal("preview changed authoritative ownership")
	}
	_, replayPreview, _, err := service.Handle("/GachaBuyPreview", previewRequest)
	if err != nil || !bytes.Equal(preview, replayPreview) {
		t.Fatalf("same preview request was not idempotent: err=%v", err)
	}
	// session.Server activates every SessionAware handler before every request.
	// Re-activating the same login between preview and confirmation must not
	// discard the server-owned result selected by the player.
	service.BeginSession("first-session")

	buyRequest := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 12), 2, 20), 3, 1)
	code, response, handled, err := service.Handle("/GachaBuy", buyRequest)
	if err != nil || !handled || code != 146 {
		t.Fatalf("confirm code=%d handled=%v err=%v", code, handled, err)
	}
	bundle, found, err = wire.Bytes(response, 1)
	if err != nil || !found || countFields(bundle, 3) != 5 || countFields(bundle, 4) != 5 {
		t.Fatalf("confirm costumes=%d equipment=%d found=%v err=%v", countFields(bundle, 3), countFields(bundle, 4), found, err)
	}
	if !service.FirstGachaCompleted() || len(collection.Costumes()) != 5 || len(equipment.All()) != 5 {
		t.Fatalf("confirmed state completed=%v costumes=%d equipment=%d", service.FirstGachaCompleted(), len(collection.Costumes()), len(equipment.All()))
	}
	if user := collection.GachaUser(2); user.TotalBuyCount != 10 || user.Point != 0 {
		t.Fatalf("first gacha accounting=%+v", user)
	}
	for i, costume := range collection.Costumes() {
		if costume.SortID != uint64(i*2) {
			t.Fatalf("costume %d sort=%d want=%d", costume.ID, costume.SortID, i*2)
		}
	}
	_, replay, _, err := service.Handle("/GachaBuy", buyRequest)
	if err != nil || !bytes.Equal(response, replay) || len(collection.Costumes()) != 5 || len(equipment.All()) != 5 {
		t.Fatalf("confirm replay mutated state: costumes=%d equipment=%d err=%v", len(collection.Costumes()), len(equipment.All()), err)
	}
	service.BeginSession("second-session")
	if _, _, _, err := service.Handle("/GachaBuyPreview", wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 20)); err == nil {
		t.Fatal("completed first gacha reopened in a new session")
	}
}

func TestFirstGachaRejectsConfirmationWithoutPreview(t *testing.T) {
	// The full happy-path test proves attachment. This assertion locks down the
	// security boundary: a forged buy cannot skip the server-owned reroll.
	service := newFirstGachaTestService(t)
	request := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 20), 3, 1)
	if _, _, handled, err := service.Handle("/GachaBuy", request); err == nil || !handled {
		t.Fatalf("direct confirmation handled=%v err=%v", handled, err)
	}
}

func newFirstGachaTestService(t *testing.T) *Service {
	t.Helper()
	characters := map[uint64]gamedata.CharacterDesign{61001: fixtureCharacter(6100, 100)}
	program := &gamedata.FirstGachaRewardGroup{ID: 20, DropCount: 10, Entries: []gamedata.FirstGachaRewardEntry{{ItemType: 11, ItemID: 61001, Count: 1, Weight: 1}}}
	first, err := gamedata.NewFirstGachaDesign(gamedata.GachaGroupDesign{ID: 2, GachaSubType: 3, TenTimeGachaID: 20}, 20, 10, program, characters, nil)
	if err != nil {
		t.Fatal(err)
	}
	infinite, err := gamedata.NewInfiniteGachaDesign(10, []uint64{61001}, characters)
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{1: {ID: 1, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 61001, Weight: 1}}}}, characters)
	if err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	collection, _ := player.OpenCollectionStore(storage, nil)
	wallet, _ := player.OpenWallet(storage, player.Currency{})
	equipment, _ := player.OpenEquipmentInventory(storage)
	service, err := NewService(infinite, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AttachFirstGacha(first); err != nil {
		t.Fatal(err)
	}
	service.AttachEquipmentGacha(first.EquipmentCatalog(), equipment)
	service.BeginSession("first-no-preview")
	return service
}
