package gacha

import (
	"testing"

	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/stateio"
	"bd2server/internal/wire"
)

func TestGachaPointExchangeGrantsUpgradesOverflowsAndRetries(t *testing.T) {
	const (
		groupID  = uint64(205)
		gachaID  = uint64(11000145)
		rollID   = uint64(60901)
		pickupID = uint64(21201)
		cost     = uint64(200)
	)
	characters := map[uint64]gamedata.CharacterDesign{
		rollID:   {ID: 6090, HP: 253, CostumeMaxLevel: 100, OverflowItemType: 20, OverflowItemCount: 2},
		pickupID: {ID: 2120, HP: 300, CostumeMaxLevel: 5, OverflowItemType: 20, OverflowItemCount: 2},
	}
	infinite, err := gamedata.NewInfiniteGachaDesign(10, []uint64{rollID}, characters)
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{
		gachaID: {ID: gachaID, Count: 10, PriceType: 3, Price: 2000, Pool: []gamedata.WeightedCostume{{ID: rollID, Weight: 1}}},
	}, characters)
	if err != nil {
		t.Fatal(err)
	}
	group := gamedata.GachaGroupDesign{
		ID: groupID, GachaType: 1, PointCount: 140,
		PickUpExchangeCost: cost, PickUpCostumeID: pickupID, TenTimeGachaID: gachaID,
	}
	if err := regular.AddGroupDesign(group, gamedata.GachaFixedDesign{}); err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := make([]uint64, 10)
	for i := range seed {
		seed[i] = rollID
	}
	if _, err := collection.GrantRegularPurchase("point-seed", seed, regular, player.GachaPurchase{Group: group, BuyType: 1}); err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(infinite, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	service.BeginSession("point-costume-login")

	var lastRequest, lastResponse []byte
	for exchange := uint64(0); exchange < 7; exchange++ {
		request := wire.AppendVarint(nil, 1, 100+exchange)
		request = wire.AppendVarint(request, 2, groupID)
		code, response, handled, err := service.Handle("/GachaPointExchange", request)
		if err != nil || !handled || code != 147 {
			t.Fatalf("exchange %d code=%d handled=%v err=%v", exchange, code, handled, err)
		}
		bundle, found, err := wire.Bytes(response, 1)
		if err != nil || !found {
			t.Fatalf("exchange %d reward bundle found=%v err=%v", exchange, found, err)
		}
		switch {
		case exchange == 0:
			if countFields(bundle, 2) != 1 || countFields(bundle, 3) != 1 || countFields(bundle, 9) != 0 {
				t.Fatalf("new pickup reward characters=%d costumes=%d upgrades=%d bundle=%x", countFields(bundle, 2), countFields(bundle, 3), countFields(bundle, 9), bundle)
			}
		case exchange < 6:
			if countFields(bundle, 3) != 0 || countFields(bundle, 9) != 1 || countFields(bundle, 8) != 0 {
				t.Fatalf("upgrade %d costumes=%d upgrades=%d exchanges=%d bundle=%x", exchange, countFields(bundle, 3), countFields(bundle, 9), countFields(bundle, 8), bundle)
			}
		default:
			if countFields(bundle, 9) != 1 || countFields(bundle, 8) != 1 || countFields(bundle, 10) != 1 {
				t.Fatalf("overflow upgrades=%d exchanges=%d repaid=%d bundle=%x", countFields(bundle, 9), countFields(bundle, 8), countFields(bundle, 10), bundle)
			}
		}
		lastRequest, lastResponse = request, response
	}
	if user := collection.GachaUser(groupID); user.Point != 0 || user.ExchangeItemCount != 7 || user.ExchangeMileageCount != 0 {
		t.Fatalf("gacha user after exchanges=%+v", user)
	}
	if got := wallet.Snapshot().Mileage; got != 2 {
		t.Fatalf("overflow mileage=%d want=2", got)
	}
	owned := collection.Costumes()
	if len(owned) != 2 || owned[1].ID != pickupID || owned[1].Level != 5 {
		t.Fatalf("owned costumes=%+v", owned)
	}

	// A byte-identical transport retry must recover the persisted grant and
	// wallet credit without another debit, level change, or exchange count.
	code, replay, handled, err := service.Handle("/GachaPointExchange", lastRequest)
	if err != nil || !handled || code != 147 || string(replay) != string(lastResponse) {
		t.Fatalf("retry code=%d handled=%v err=%v response=%x want=%x", code, handled, err, replay, lastResponse)
	}
	if user := collection.GachaUser(groupID); user.Point != 0 || user.ExchangeItemCount != 7 {
		t.Fatalf("retry changed gacha user=%+v", user)
	}
	if got := wallet.Snapshot().Mileage; got != 2 {
		t.Fatalf("retry changed mileage=%d", got)
	}

	restored, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewService(infinite, regular, restored, wallet)
	if err != nil {
		t.Fatal(err)
	}
	restarted.BeginSession("point-costume-login")
	_, replay, _, err = restarted.Handle("/GachaPointExchange", lastRequest)
	if err != nil || string(replay) != string(lastResponse) {
		t.Fatalf("restart retry err=%v response=%x want=%x", err, replay, lastResponse)
	}
	if user := restored.GachaUser(groupID); user.Point != 0 || user.ExchangeItemCount != 7 {
		t.Fatalf("restored gacha user=%+v", user)
	}
}

func TestGachaPointExchangeUsesOnlySavedSingleSelection(t *testing.T) {
	const (
		groupID  = uint64(1001)
		gachaID  = uint64(11000001)
		rollID   = uint64(60901)
		selected = uint64(64901)
	)
	characters := map[uint64]gamedata.CharacterDesign{
		rollID:   fixtureCharacter(6090, 253),
		selected: fixtureCharacter(6490, 300),
	}
	infinite, err := gamedata.NewInfiniteGachaDesign(10, []uint64{rollID}, characters)
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{
		gachaID: {ID: gachaID, Count: 1, PriceType: 3, Price: 200, Pool: []gamedata.WeightedCostume{{ID: rollID, Weight: 1}}},
	}, characters)
	if err != nil {
		t.Fatal(err)
	}
	group := gamedata.GachaGroupDesign{ID: groupID, GachaType: 1, PointCount: 200, PickUpExchangeCost: 200, OneTimeGachaID: gachaID, SelectCount: 1}
	if err := regular.AddGroupDesign(group, gamedata.GachaFixedDesign{}); err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collection.GrantRegularPurchase("selection-point-seed", []uint64{rollID}, regular, player.GachaPurchase{Group: group, BuyType: 1}); err != nil {
		t.Fatal(err)
	}
	if err := collection.SaveGachaSelections(groupID, []player.GachaSelection{{GroupID: groupID, Slot: 1, ItemID: selected}}, 0); err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(infinite, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	service.BeginSession("selection-point-login")

	bad := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, groupID), 3, rollID)
	if code, _, handled, err := service.Handle("/GachaPointExchange", bad); err == nil || !handled || code != 147 {
		t.Fatalf("unsaved selection code=%d handled=%v err=%v", code, handled, err)
	}
	if user := collection.GachaUser(groupID); user.Point != 200 || user.ExchangeItemCount != 0 {
		t.Fatalf("rejected selection changed user=%+v", user)
	}

	request := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, groupID), 3, selected)
	code, response, handled, err := service.Handle("/GachaPointExchange", request)
	if err != nil || !handled || code != 147 {
		t.Fatalf("saved selection code=%d handled=%v err=%v", code, handled, err)
	}
	bundle, found, err := wire.Bytes(response, 1)
	if err != nil || !found || countFields(bundle, 3) != 1 {
		t.Fatalf("saved selection reward found=%v costumes=%d err=%v bundle=%x", found, countFields(bundle, 3), err, bundle)
	}
	if user := collection.GachaUser(groupID); user.Point != 0 || user.ExchangeItemCount != 1 {
		t.Fatalf("saved selection user=%+v", user)
	}
}
