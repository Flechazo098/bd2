package gacha

import (
	"os"
	"testing"
	"time"

	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/stateio"
	"bd2server/internal/wire"
)

func TestTicketOnlyEquipmentDrawUsesGameDataAndNoScheduleAccounting(t *testing.T) {
	root := os.Getenv("BD2_TEST_GAMEDATA_ROOT")
	if root == "" {
		t.Skip("set BD2_TEST_GAMEDATA_ROOT for installed GameData integration test")
	}
	const version = "20260910162539"
	infinite, err := gamedata.LoadInfiniteGacha(root, version)
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.LoadRegularCostumeGacha(root, version)
	if err != nil {
		t.Fatal(err)
	}
	equipmentCatalog, err := gamedata.LoadEquipmentGacha(root, version)
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
	inventory, err := player.OpenInventory(storage, &player.Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	granted, err := inventory.GrantOnce("ticket", []gamedata.BattleReward{{Type: 8, ID: 1104, Count: 20}})
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
	service.AttachInventory(inventory)
	service.AttachEquipmentGacha(equipmentCatalog, equipment)
	service.BeginSession("ticket-only")
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 7), 2, 71200001)
	request = wire.AppendVarint(request, 3, 1)
	ticket := granted[0]
	ticket.Count = 10
	request = wire.AppendBytes(request, 4, player.ItemWire(ticket))
	code, response, handled, err := service.Handle("/GachaBuy", request)
	if err != nil || !handled || code != 146 {
		t.Fatalf("ticket-only draw code=%d handled=%v err=%v", code, handled, err)
	}
	bundle, found, err := wire.Bytes(response, 1)
	if err != nil || !found || countFields(bundle, 4) != 10 {
		t.Fatalf("ticket-only reward equipment=%d found=%v err=%v", countFields(bundle, 4), found, err)
	}
	if len(equipment.All()) != 10 {
		t.Fatalf("ticket-only persisted equipment=%d", len(equipment.All()))
	}
	if user := collection.GachaUser(71200001); user != (player.GachaUserState{}) {
		t.Fatalf("standalone draw invented schedule accounting: %+v", user)
	}
	remaining := inventory.All()
	foundTicket := false
	for _, item := range remaining {
		if item.InvenIndex == ticket.InvenIndex {
			foundTicket = true
			if item.Count != 10 {
				t.Fatalf("ticket count=%d want=10", item.Count)
			}
		}
	}
	if !foundTicket {
		t.Fatal("remaining ticket stack missing")
	}
	_, replay, _, err := service.Handle("/GachaBuy", request)
	if err != nil || string(replay) != string(response) || len(equipment.All()) != 10 {
		t.Fatalf("ticket-only replay changed result: equipment=%d err=%v", len(equipment.All()), err)
	}
}

func TestCostumeFixedStatesOmitDisabledThresholds(t *testing.T) {
	result := gamedata.GachaFixedRoll{
		CostumeGrade4Count: 7,
		CostumeGrade5Count: 8,
		CostumeGrade4Sort:  2,
		CostumeGrade5Sort:  3,
	}

	fiveOnly := costumeFixedStates(gamedata.GachaFixedDesign{ID: 3, CostumeGrade5Count: 10}, result)
	if len(fiveOnly) != 1 || fiveOnly[0].FixedID != 3 || fiveOnly[0].Type != 1 || fiveOnly[0].Count != 8 || fiveOnly[0].ApplySort != 3 {
		t.Fatalf("five-only states=%+v", fiveOnly)
	}
	fourOnly := costumeFixedStates(gamedata.GachaFixedDesign{ID: 5, CostumeGrade4Count: 10}, result)
	if len(fourOnly) != 1 || fourOnly[0].FixedID != 5 || fourOnly[0].Type != 0 || fourOnly[0].Count != 7 || fourOnly[0].ApplySort != 2 {
		t.Fatalf("four-only states=%+v", fourOnly)
	}
	both := costumeFixedStates(gamedata.GachaFixedDesign{ID: 1, CostumeGrade4Count: 10, CostumeGrade5Count: 100}, result)
	if len(both) != 2 || both[0].Type != 0 || both[1].Type != 1 {
		t.Fatalf("two-threshold states=%+v", both)
	}
}

func fixtureCharacter(id, hp uint64) gamedata.CharacterDesign {
	return gamedata.CharacterDesign{
		ID: id, HP: hp, CostumeMaxLevel: 5,
		OverflowItemType: 20, OverflowItemCount: 2,
	}
}

func TestInfinitePreviewAndFreeConfirmationPersist(t *testing.T) {
	design, err := gamedata.NewInfiniteGachaDesign(10, []uint64{60901}, map[uint64]gamedata.CharacterDesign{
		60901: fixtureCharacter(6090, 253),
	})
	if err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{1: {ID: 1, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 60901, Weight: 1}}}}, map[uint64]gamedata.CharacterDesign{60901: fixtureCharacter(6090, 253)})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AttachPreviewEventIndex(1171); err != nil {
		t.Fatal(err)
	}
	schedule := &ScheduleSeed{
		ClientVersion: "test",
		Schedules: []ScheduleWindow{
			{GroupID: 166, StartTime: 10, EndTime: 20},
			{GroupID: infiniteScheduleGroupID, StartTime: 10, EndTime: 20},
			{GroupID: paidTwelvePickGroupID, StartTime: 10, EndTime: 20},
			{GroupID: 206, StartTime: 10, EndTime: 20},
		},
		StepUps: []ScheduleWindow{{GroupID: 29, StartTime: 10, EndTime: 20}},
	}
	if err := service.AttachSchedule(schedule); err != nil {
		t.Fatal(err)
	}
	preview := wire.AppendVarint(nil, 1, 1)
	preview = wire.AppendVarint(preview, 2, gamedata.InfiniteGachaID)
	preview = wire.AppendVarint(preview, 3, gamedata.InfiniteProductGroupID)
	preview = wire.AppendVarint(preview, 4, gamedata.InfiniteProductID)
	code, response, ok, err := service.Handle("/GachaBuyPreview", preview)
	if err != nil || !ok || code != 175 {
		t.Fatalf("preview code=%d ok=%v err=%v", code, ok, err)
	}
	bundle, found, err := wire.Bytes(response, 1)
	if err != nil || !found || countFields(bundle, 3) != 10 {
		t.Fatalf("preview bundle fields=%d found=%v err=%v", countFields(bundle, 3), found, err)
	}
	if len(collection.Characters()) != 0 {
		t.Fatal("preview changed ownership")
	}
	eventIndex, locked := collection.PreviewLock()
	if locked || eventIndex != 1171 {
		t.Fatalf("new preview event=%d locked=%v", eventIndex, locked)
	}
	_, infoBeforeLock, _, err := service.Handle("/GachaInfo", wire.AppendVarint(nil, 1, 2))
	if got := collectScheduleWindows(t, infoBeforeLock, 1); !sameScheduleGroups(got, 166, infiniteScheduleGroupID, paidTwelvePickGroupID, 206) {
		t.Fatalf("pre-purchase schedule groups=%v", scheduleGroupIDs(got))
	}
	if got := collectScheduleWindows(t, infoBeforeLock, 7); !sameScheduleGroups(got, 29) {
		t.Fatalf("pre-purchase step-up groups=%v", scheduleGroupIDs(got))
	}
	previewState, found, parseErr := wire.Bytes(infoBeforeLock, 9)
	gotEvent, eventFound, eventErr := wire.Varint(previewState, 1)
	previewBundle, bundleFound, bundleErr := wire.Bytes(previewState, 8)
	if err != nil || parseErr != nil || eventErr != nil || bundleErr != nil || !found || !eventFound || !bundleFound || gotEvent != eventIndex || countFields(previewBundle, 3) != 10 {
		t.Fatalf("gacha info preview event=%d items=%d found=%v/%v/%v err=%v/%v/%v/%v", gotEvent, countFields(previewBundle, 3), found, eventFound, bundleFound, err, parseErr, eventErr, bundleErr)
	}
	lock := wire.AppendVarint(nil, 1, 2)
	lock = wire.AppendVarint(lock, 2, eventIndex)
	code, response, ok, err = service.Handle("/GachaBuyPreviewLock", lock)
	if err != nil || !ok || code != 175 || len(response) != 0 {
		t.Fatalf("preview lock code=%d bytes=%d ok=%v err=%v", code, len(response), ok, err)
	}
	if got, locked := collection.PreviewLock(); !locked || got != eventIndex {
		t.Fatalf("preview lock event=%d locked=%v", got, locked)
	}

	buy := wire.AppendVarint(nil, 1, 3)
	buy = wire.AppendVarint(buy, 3, gamedata.InfiniteProductGroupID)
	product := wire.AppendVarint(nil, 1, gamedata.InfiniteProductID)
	product = wire.AppendVarint(product, 3, 1)
	buy = wire.AppendBytes(buy, 4, product)
	code, response, ok, err = service.Handle("/CashShopBuy", buy)
	if err != nil || !ok || code != 61 {
		t.Fatalf("confirm code=%d ok=%v err=%v", code, ok, err)
	}
	if service.FirstGachaCompleted() {
		t.Fatal("infinite product incorrectly completed the subtype-3 first pick")
	}
	reward, found, err := wire.Bytes(response, 1)
	if err != nil || !found || countFields(reward, 2) != 1 || countFields(reward, 3) != 1 || countFields(reward, 9) != 9 || countFields(reward, 8) != 4 {
		t.Fatalf("reward chars=%d costumes=%d upgrades=%d exchanges=%d found=%v err=%v", countFields(reward, 2), countFields(reward, 3), countFields(reward, 9), countFields(reward, 8), found, err)
	}
	restored, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.Characters(); len(got) != 1 || got[0].ID != 6090 {
		t.Fatalf("restored characters=%+v", got)
	}
	if got := restored.Costumes(); len(got) != 1 || got[0].ID != 60901 || got[0].Level != 5 {
		t.Fatalf("restored costumes=%+v", got)
	}
	if got := wallet.Snapshot().Mileage; got != 8 {
		t.Fatalf("mileage=%d want=8", got)
	}
	if got, locked := restored.PreviewLock(); !locked || got != eventIndex {
		t.Fatalf("restored preview lock event=%d locked=%v", got, locked)
	}
	code, response, ok, err = service.Handle("/GachaInfo", wire.AppendVarint(nil, 1, 4))
	postPurchaseSchedules := collectScheduleWindows(t, response, 1)
	postPurchaseStepUps := collectScheduleWindows(t, response, 7)
	if err != nil || !ok || code != 145 || countFields(response, 9) != 0 || !sameScheduleGroups(postPurchaseSchedules, 166, paidTwelvePickGroupID, 206) || !sameScheduleGroups(postPurchaseStepUps, 29) {
		t.Fatalf("post-purchase info code=%d schedules=%v step-ups=%v preview=%d ok=%v err=%v", code, scheduleGroupIDs(postPurchaseSchedules), scheduleGroupIDs(postPurchaseStepUps), countFields(response, 9), ok, err)
	}
	if !sameScheduleGroups(schedule.Schedules, 166, infiniteScheduleGroupID, paidTwelvePickGroupID, 206) {
		t.Fatalf("account filtering mutated public seed=%v", scheduleGroupIDs(schedule.Schedules))
	}
	code, response, ok, err = service.Handle("/CashShopPurchaseCountInfo", wire.AppendVarint(nil, 1, 5))
	countInfo, found, parseErr := wire.Bytes(response, 1)
	productGroup, groupFound, groupErr := wire.Varint(countInfo, 1)
	productID, productFound, productErr := wire.Varint(countInfo, 2)
	_, saleGroupFound, saleGroupErr := wire.Varint(countInfo, 3)
	count, countFound, countErr := wire.Varint(countInfo, 4)
	if err != nil || parseErr != nil || groupErr != nil || productErr != nil || saleGroupErr != nil || countErr != nil || !ok || code != 432 || countFields(response, 1) != 1 || !found || !groupFound || !productFound || saleGroupFound || !countFound || productGroup != gamedata.InfiniteProductGroupID || productID != gamedata.InfiniteProductID || count != 1 {
		t.Fatalf("purchase count code=%d group=%d product=%d saleGroupFound=%v count=%d found=%v/%v/%v/%v ok=%v err=%v/%v/%v/%v/%v/%v", code, productGroup, productID, saleGroupFound, count, found, groupFound, productFound, countFound, ok, err, parseErr, groupErr, productErr, saleGroupErr, countErr)
	}
	if _, _, _, err := service.Handle("/GachaBuyPreview", preview); err == nil {
		t.Fatal("purchased infinite gacha accepted a new preview")
	}
	_, infoAfterRejectedPreview, _, err := service.Handle("/GachaInfo", wire.AppendVarint(nil, 1, 6))
	if err != nil || !sameScheduleGroups(collectScheduleWindows(t, infoAfterRejectedPreview, 1), 166, paidTwelvePickGroupID, 206) || countFields(infoAfterRejectedPreview, 9) != 0 {
		t.Fatalf("rejected preview changed post-purchase availability: err=%v", err)
	}
}

func sameScheduleGroups(windows []ScheduleWindow, want ...uint64) bool {
	if len(windows) != len(want) {
		return false
	}
	for i := range windows {
		if windows[i].GroupID != want[i] {
			return false
		}
	}
	return true
}

func scheduleGroupIDs(windows []ScheduleWindow) []uint64 {
	groups := make([]uint64, len(windows))
	for i := range windows {
		groups[i] = windows[i].GroupID
	}
	return groups
}

func TestInfinitePreviewMustBeLockedAndRerollClearsLock(t *testing.T) {
	design, err := gamedata.NewInfiniteGachaDesign(10, []uint64{60901}, map[uint64]gamedata.CharacterDesign{60901: fixtureCharacter(6090, 253)})
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{1: {ID: 1, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 60901, Weight: 1}}}}, map[uint64]gamedata.CharacterDesign{60901: fixtureCharacter(6090, 253)})
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
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AttachPreviewEventIndex(1171); err != nil {
		t.Fatal(err)
	}
	preview := wire.AppendVarint(nil, 1, 1)
	preview = wire.AppendVarint(preview, 2, gamedata.InfiniteGachaID)
	preview = wire.AppendVarint(preview, 3, gamedata.InfiniteProductGroupID)
	preview = wire.AppendVarint(preview, 4, gamedata.InfiniteProductID)
	if _, _, _, err := service.Handle("/GachaBuyPreview", preview); err != nil {
		t.Fatal(err)
	}
	firstEvent, locked := collection.PreviewLock()
	if locked || firstEvent != 1171 {
		t.Fatalf("first preview event=%d locked=%v", firstEvent, locked)
	}
	buy := wire.AppendVarint(nil, 1, 2)
	buy = wire.AppendVarint(buy, 3, gamedata.InfiniteProductGroupID)
	product := wire.AppendVarint(nil, 1, gamedata.InfiniteProductID)
	product = wire.AppendVarint(product, 3, 1)
	buy = wire.AppendBytes(buy, 4, product)
	if _, _, _, err := service.Handle("/CashShopBuy", buy); err == nil {
		t.Fatal("unlocked preview was confirmed")
	}
	lock := wire.AppendVarint(nil, 1, 3)
	lock = wire.AppendVarint(lock, 2, firstEvent)
	if _, _, _, err := service.Handle("/GachaBuyPreviewLock", lock); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Handle("/GachaBuyPreview", preview); err != nil {
		t.Fatal(err)
	}
	if eventIndex, locked := collection.PreviewLock(); locked || eventIndex != firstEvent {
		t.Fatalf("reroll event=%d locked=%v, want %d/unlocked", eventIndex, locked, firstEvent)
	}
}

func TestRegularGachaBuySpendsWalletAndPersistsCostume(t *testing.T) {
	design, err := gamedata.NewInfiniteGachaDesign(10, []uint64{60901}, map[uint64]gamedata.CharacterDesign{60901: fixtureCharacter(6090, 253)})
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{
		10100084: {ID: 10100084, Count: 1, PriceType: 3, Price: 200, Pool: []gamedata.WeightedCostume{{ID: 60901, Weight: 1}}},
	}, map[uint64]gamedata.CharacterDesign{60901: fixtureCharacter(6090, 253)})
	if err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{FreeJewelry: 500})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 77)
	request = wire.AppendVarint(request, 2, 10100084)
	request = wire.AppendVarint(request, 3, 1)
	code, response, ok, err := service.Handle("/GachaBuy", request)
	if err != nil || !ok || code != 146 {
		t.Fatalf("code=%d ok=%v err=%v", code, ok, err)
	}
	if wallet.Snapshot().FreeJewelry != 300 {
		t.Fatalf("wallet=%+v", wallet.Snapshot())
	}
	bundle, found, _ := wire.Bytes(response, 1)
	if !found || countFields(bundle, 2) != 1 || countFields(bundle, 3) != 1 {
		t.Fatalf("bundle=%x", bundle)
	}
	if _, _, _, err := service.Handle("/GachaBuy", request); err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().FreeJewelry != 300 || len(collection.Characters()) != 1 {
		t.Fatalf("retry wallet=%+v chars=%d", wallet.Snapshot(), len(collection.Characters()))
	}
}

func TestRegularGachaSequenceReuseAcrossLoginIsANewPurchase(t *testing.T) {
	design, err := gamedata.NewInfiniteGachaDesign(10, []uint64{60901}, map[uint64]gamedata.CharacterDesign{60901: fixtureCharacter(6090, 253)})
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{
		10100084: {ID: 10100084, Count: 1, PriceType: 3, Price: 200, Pool: []gamedata.WeightedCostume{{ID: 60901, Weight: 1}}},
	}, map[uint64]gamedata.CharacterDesign{60901: fixtureCharacter(6090, 253)})
	if err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{FreeJewelry: 400})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, 10100084)
	request = wire.AppendVarint(request, 3, 1)
	service.BeginSession("login-a")
	if _, _, _, err := service.Handle("/GachaBuy", request); err != nil {
		t.Fatal(err)
	}
	// Same login + sequence is an HTTP retry.
	if _, _, _, err := service.Handle("/GachaBuy", request); err != nil {
		t.Fatal(err)
	}
	if got := wallet.Snapshot().FreeJewelry; got != 200 {
		t.Fatalf("retry balance=%d want=200", got)
	}
	service.BeginSession("login-b")
	if _, _, _, err := service.Handle("/GachaBuy", request); err != nil {
		t.Fatal(err)
	}
	if got := wallet.Snapshot().FreeJewelry; got != 0 {
		t.Fatalf("new-login balance=%d want=0", got)
	}
	if got := collection.Costumes(); len(got) != 1 || got[0].Level != 1 {
		t.Fatalf("costumes=%+v", got)
	}
}

func TestPaidRegularGachaBuySpendsPaidJewelry(t *testing.T) {
	design, err := gamedata.NewInfiniteGachaDesign(10, []uint64{60901}, map[uint64]gamedata.CharacterDesign{60901: fixtureCharacter(6090, 253)})
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{
		8100118: {ID: 8100118, Count: 10, PriceType: 2, Price: 2000, Pool: []gamedata.WeightedCostume{{ID: 60901, Weight: 1}}},
	}, map[uint64]gamedata.CharacterDesign{60901: fixtureCharacter(6090, 253)})
	if err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{Jewelry: 5000})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 88)
	request = wire.AppendVarint(request, 2, 8100118)
	// GB_NORMAL uses GachaTable.PriceType to distinguish paid (2) from free
	// jewelry (3); it is not itself the paid/free currency selector.
	request = wire.AppendVarint(request, 3, 1)
	code, response, ok, err := service.Handle("/GachaBuy", request)
	if err != nil || !ok || code != 146 {
		t.Fatalf("code=%d ok=%v err=%v", code, ok, err)
	}
	if wallet.Snapshot().Jewelry != 3000 || wallet.Snapshot().FreeJewelry != 0 {
		t.Fatalf("wallet=%+v", wallet.Snapshot())
	}
	bundle, found, _ := wire.Bytes(response, 1)
	if !found || countFields(bundle, 3) != 1 || countFields(bundle, 9) != 9 || countFields(bundle, 8) != 4 {
		t.Fatalf("bundle=%x", bundle)
	}
}

func TestPriceType19IsRejectedWithoutChargingDiamonds(t *testing.T) {
	character := fixtureCharacter(6090, 253)
	design, err := gamedata.NewInfiniteGachaDesign(10, []uint64{60901}, map[uint64]gamedata.CharacterDesign{60901: character})
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{
		1: {ID: 1, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 60901, Weight: 1}}},
	}, map[uint64]gamedata.CharacterDesign{60901: character})
	if err != nil {
		t.Fatal(err)
	}
	regular.Gachas[9100037] = gamedata.RegularGacha{ID: 9100037, Count: 10, PriceType: 19, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 60901, Weight: 1}}}
	storage := stateio.NewMemory()
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{FreeJewelry: 9999, Jewelry: 9999})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, 9100037)
	request = wire.AppendVarint(request, 3, 1)
	if _, _, handled, err := service.Handle("/GachaBuy", request); !handled || err == nil {
		t.Fatalf("priceType19 handled=%v err=%v", handled, err)
	}
	if got := wallet.Snapshot(); got.FreeJewelry != 9999 || got.Jewelry != 9999 {
		t.Fatalf("unsupported purchase charged wallet: %+v", got)
	}
	if len(collection.Characters()) != 0 || len(collection.Costumes()) != 0 {
		t.Fatal("unsupported purchase granted collection items")
	}
}

func TestUnverifiedGachaRPCsRemainUnhandled(t *testing.T) {
	character := fixtureCharacter(6090, 253)
	design, err := gamedata.NewInfiniteGachaDesign(10, []uint64{60901}, map[uint64]gamedata.CharacterDesign{60901: character})
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{
		1: {ID: 1, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 60901, Weight: 1}}},
	}, map[uint64]gamedata.CharacterDesign{60901: character})
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
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	for _, path := range []string{"/GachaMultiBuy", "/GachaLog"} {
		if code, response, handled, err := service.Handle(path, request); err != nil || handled || code != 0 || response != nil {
			t.Fatalf("%s code=%d bytes=%x handled=%v err=%v", path, code, response, handled, err)
		}
	}
}

func TestCompletedStepUpPersistsAndRemainsVisible(t *testing.T) {
	const stepUpGroupID = 29
	stepUpGachaIDs := []uint64{8100118, 8100119, 8100120, 8100121}
	character := fixtureCharacter(6090, 253)
	design, err := gamedata.NewInfiniteGachaDesign(10, []uint64{60901}, map[uint64]gamedata.CharacterDesign{60901: character})
	if err != nil {
		t.Fatal(err)
	}
	gachas := make(map[uint64]gamedata.RegularGacha, len(stepUpGachaIDs))
	for _, id := range stepUpGachaIDs {
		gachas[id] = gamedata.RegularGacha{ID: id, Count: 1, PriceType: 2, Price: 200, Pool: []gamedata.WeightedCostume{{ID: 60901, Weight: 1}}}
	}
	regular, err := gamedata.NewRegularGachaCatalog(gachas, map[uint64]gamedata.CharacterDesign{60901: character})
	if err != nil {
		t.Fatal(err)
	}
	steps := gamedata.GachaStepUpDesign{ID: stepUpGroupID}
	for i, id := range stepUpGachaIDs {
		steps.Steps = append(steps.Steps, gamedata.GachaStepDesign{Step: uint64(i + 1), GroupID: uint64(20118 + i), GachaID: id, FixedID: 5})
	}
	if err := regular.AddStepUpDesign(steps); err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{Jewelry: 1000})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	service.BeginSession("first-login")
	for i, id := range stepUpGachaIDs {
		request := wire.AppendVarint(nil, 1, uint64(i+1))
		request = wire.AppendVarint(request, 2, id)
		request = wire.AppendVarint(request, 3, 1)
		if _, _, _, err := service.Handle("/GachaBuy", request); err != nil {
			t.Fatalf("step %d: %v", i+1, err)
		}
	}
	if got := collection.StepUpProgress(stepUpGroupID); got != 4 {
		t.Fatalf("progress=%d want=4", got)
	}
	restored, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewService(design, regular, restored, wallet)
	if err != nil {
		t.Fatal(err)
	}
	restarted.BeginSession("second-login")
	if err := restarted.AttachSchedule(&ScheduleSeed{ClientVersion: "test", Schedules: []ScheduleWindow{{GroupID: 1, StartTime: 1, EndTime: 2}}, StepUps: []ScheduleWindow{{GroupID: stepUpGroupID, StartTime: 1, EndTime: 2}}}); err != nil {
		t.Fatal(err)
	}
	_, info, _, err := restarted.Handle("/GachaInfo", wire.AppendVarint(nil, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	if countFields(info, 7) != 1 || countFields(info, 8) != 1 {
		t.Fatalf("step-up schedule/user count=%d/%d info=%x", countFields(info, 7), countFields(info, 8), info)
	}
	user, found, err := wire.Bytes(info, 8)
	if err != nil || !found {
		t.Fatalf("step user found=%v err=%v", found, err)
	}
	group, _, _ := wire.Varint(user, 1)
	completed, _, _ := wire.Varint(user, 2)
	if group != stepUpGroupID || completed != 4 {
		t.Fatalf("step user group=%d completed=%d", group, completed)
	}
	balance := wallet.Snapshot().Jewelry
	rebuy := wire.AppendVarint(nil, 1, 2)
	rebuy = wire.AppendVarint(rebuy, 2, stepUpGachaIDs[0])
	rebuy = wire.AppendVarint(rebuy, 3, 1)
	if _, _, _, err := restarted.Handle("/GachaBuy", rebuy); err == nil {
		t.Fatal("completed step-up accepted another purchase")
	}
	if got := wallet.Snapshot().Jewelry; got != balance {
		t.Fatalf("rejected purchase changed wallet: %d -> %d", balance, got)
	}
}

func TestInstalledStepUpGroups29And30UseGenericIndependentProgress(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("set BD2_REAL_GAMEDATA for installed GameData integration test")
	}
	const version = "20260923193640"
	design, err := gamedata.LoadInfiniteGacha(root, version)
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.LoadRegularCostumeGachaGroups(root, version, nil, []uint64{29, 30})
	if err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{Jewelry: 20000})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	sequence := uint64(100)
	for _, groupID := range []uint64{29, 30} {
		stepUp, ok := regular.StepUp(groupID)
		if !ok || len(stepUp.Steps) != 4 {
			t.Fatalf("step-up group %d=%+v ok=%v", groupID, stepUp, ok)
		}
		sequence++
		wrong := wire.AppendVarint(nil, 1, sequence)
		wrong = wire.AppendVarint(wrong, 2, stepUp.Steps[0].GachaID)
		wrong = wire.AppendVarint(wrong, 3, 2)
		before := wallet.Snapshot().Jewelry
		if _, _, handled, err := service.Handle("/GachaBuy", wrong); !handled || err == nil {
			t.Fatalf("step-up group %d accepted GB_CASH handled=%v err=%v", groupID, handled, err)
		}
		if got := wallet.Snapshot().Jewelry; got != before {
			t.Fatalf("rejected GB_CASH changed jewelry %d -> %d", before, got)
		}
		for _, step := range stepUp.Steps {
			sequence++
			request := wire.AppendVarint(nil, 1, sequence)
			request = wire.AppendVarint(request, 2, step.GachaID)
			request = wire.AppendVarint(request, 3, 1)
			code, _, handled, err := service.Handle("/GachaBuy", request)
			if err != nil || !handled || code != 146 {
				t.Fatalf("group=%d step=%d gacha=%d code=%d handled=%v err=%v", groupID, step.Step, step.GachaID, code, handled, err)
			}
			if got := collection.StepUpProgress(groupID); got != step.Step {
				t.Fatalf("group=%d progress=%d want=%d", groupID, got, step.Step)
			}
		}
	}
	if collection.StepUpProgress(29) != 4 || collection.StepUpProgress(30) != 4 {
		t.Fatalf("independent progress 29=%d 30=%d", collection.StepUpProgress(29), collection.StepUpProgress(30))
	}
	for _, groupID := range []uint64{29, 30} {
		stepUp, _ := regular.StepUp(groupID)
		for _, step := range stepUp.Steps {
			user := collection.GachaUser(step.GroupID)
			if user.TotalBuyCount != 10 || user.OneCashPickCount != 0 || user.TenCashPickCount != 0 {
				t.Fatalf("step-up group %d child %d counters=%+v", groupID, step.GroupID, user)
			}
		}
	}
	if got := wallet.Snapshot().Jewelry; got != 10000 {
		t.Fatalf("two step-up groups left jewelry=%d want=10000", got)
	}
}

func TestRegularTenPullContainsTenClientDisplayEntriesAtMaxLevel(t *testing.T) {
	character := fixtureCharacter(6090, 253)
	design, err := gamedata.NewInfiniteGachaDesign(10, []uint64{60901}, map[uint64]gamedata.CharacterDesign{60901: character})
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{
		11000084: {ID: 11000084, Count: 10, PriceType: 3, Price: 2000, Pool: []gamedata.WeightedCostume{{ID: 60901, Weight: 1}}},
	}, map[uint64]gamedata.CharacterDesign{60901: character})
	if err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{FreeJewelry: 2000})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, 11000084)
	request = wire.AppendVarint(request, 3, 1)
	_, response, _, err := service.Handle("/GachaBuy", request)
	if err != nil {
		t.Fatal(err)
	}
	bundle, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("reward bundle found=%v err=%v", found, err)
	}
	// Gacha result builds a slot from CostumeInfo (field 3) or
	// ItemAutoUpgradeInfo (field 9). With one new costume followed by nine
	// duplicates, all ten pulls must therefore be represented by 1 + 9.
	if got := countFields(bundle, 3) + countFields(bundle, 9); got != 10 {
		t.Fatalf("client display entries=%d costumes=%d upgrades=%d bundle=%x", got, countFields(bundle, 3), countFields(bundle, 9), bundle)
	}
	if countFields(bundle, 8) != 4 {
		t.Fatalf("overflow exchanges=%d want=4", countFields(bundle, 8))
	}
	if got := collection.Costumes()[0].Level; got != 5 {
		t.Fatalf("costume level=%d want=5", got)
	}
	grant, ok := collection.Grant("regular-gacha:11000084:seq:1")
	if !ok || len(grant.Upgrades) != 9 || len(grant.Exchanges) != 4 {
		t.Fatalf("grant=%+v", grant)
	}
	for _, exchange := range grant.Exchanges {
		matched := false
		for _, upgrade := range grant.Upgrades {
			if upgrade.CostumeID == exchange.OriginalItemID && upgrade.SortID == exchange.SortID && upgrade.Before == 5 && upgrade.After == 5 {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("exchange has no display upgrade: %+v grant=%+v", exchange, grant)
		}
	}
}

func TestGachaPointManualExchangePersistsAndRetriesWithoutDoubleGrant(t *testing.T) {
	character := fixtureCharacter(6090, 253)
	design, err := gamedata.NewInfiniteGachaDesign(10, []uint64{60901}, map[uint64]gamedata.CharacterDesign{60901: character})
	if err != nil {
		t.Fatal(err)
	}
	pool := []gamedata.WeightedCostume{
		{Weight: 1, Children: []gamedata.WeightedCostume{{ID: 60901, Weight: 1}}},
		{Weight: 1, Children: []gamedata.WeightedCostume{{ID: 60901, Weight: 1}}},
		{Weight: 1, Children: []gamedata.WeightedCostume{{ID: 60901, Weight: 1}}},
		{Weight: 1, Children: []gamedata.WeightedCostume{{ID: 60901, Weight: 1}}},
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{
		11000145: {ID: 11000145, Count: 10, PriceType: 3, Price: 2000, Pool: pool},
	}, map[uint64]gamedata.CharacterDesign{60901: character})
	if err != nil {
		t.Fatal(err)
	}
	if err := regular.AddGroupDesign(gamedata.GachaGroupDesign{ID: 205, FixedID: 1, PointCount: 1, PickUpExchangeCost: 200, PickUpCostumeID: 60901, TenTimeGachaID: 11000145}, gamedata.GachaFixedDesign{ID: 1, CostumeGrade4Count: 10, CostumeGrade5Count: 100, ResetOnMatchingGrade: true}); err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{FreeJewelry: 2000})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	service.BeginSession("point-test-login")
	buy := wire.AppendVarint(nil, 1, 41)
	buy = wire.AppendVarint(buy, 2, 11000145)
	buy = wire.AppendVarint(buy, 3, 1)
	_, buyResponse, _, err := service.Handle("/GachaBuy", buy)
	if err != nil {
		t.Fatal(err)
	}
	if points, _, _ := wire.Varint(buyResponse, 2); points != 10 {
		t.Fatalf("buy points=%d want=10", points)
	}
	if user := collection.GachaUser(205); user.Point != 10 || user.TotalBuyCount != 10 {
		t.Fatalf("gacha user=%+v", user)
	}
	exchange := wire.AppendVarint(nil, 1, 42)
	exchange = wire.AppendVarint(exchange, 2, 205)
	exchange = wire.AppendVarint(exchange, 3, 5)
	for i := 0; i < 2; i++ {
		code, response, ok, err := service.Handle("/GachaPointManualExchange", exchange)
		if err != nil || !ok || code != 188 {
			t.Fatalf("exchange %d code=%d ok=%v err=%v", i, code, ok, err)
		}
		bundle, found, err := wire.Bytes(response, 1)
		if err != nil || !found || countFields(bundle, 10) != 1 {
			t.Fatalf("exchange reward bundle=%x found=%v err=%v", bundle, found, err)
		}
		view, _, _ := wire.Bytes(bundle, 10)
		itemType, _, _ := wire.Varint(view, 2)
		count, _, _ := wire.Varint(view, 3)
		if itemType != 22 || count != 5 {
			t.Fatalf("hope powder view type=%d count=%d", itemType, count)
		}
	}
	if user := collection.GachaUser(205); user.Point != 5 || user.ExchangeMileageCount != 5 {
		t.Fatalf("point exchange user=%+v", user)
	}
	if got := wallet.Snapshot().HopePowder; got != 5 {
		t.Fatalf("hope powder=%d want=5", got)
	}
	restored, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	if user := restored.GachaUser(205); user.Point != 5 || user.ExchangeMileageCount != 5 {
		t.Fatalf("restored gacha user=%+v", user)
	}
	if fixed := restored.GachaFixedStates(); len(fixed) != 2 || fixed[0].ApplySort != -1 || fixed[1].ApplySort != -1 {
		t.Fatalf("restored guaranteed counters=%+v", fixed)
	}
	restarted, err := NewService(design, regular, restored, wallet)
	if err != nil {
		t.Fatal(err)
	}
	restarted.BeginSession("new-login")
	_, info, _, err := restarted.Handle("/GachaInfo", wire.AppendVarint(nil, 1, 43))
	if err != nil || countFields(info, 2) != 1 || countFields(info, 4) != 2 {
		t.Fatalf("gacha info users=%d fixed=%d err=%v", countFields(info, 2), countFields(info, 4), err)
	}
}

func TestCashShopPurchaseCountStartsEmpty(t *testing.T) {
	design, err := gamedata.NewInfiniteGachaDesign(10, []uint64{60901}, map[uint64]gamedata.CharacterDesign{60901: fixtureCharacter(6090, 253)})
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{1: {ID: 1, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 60901, Weight: 1}}}}, map[uint64]gamedata.CharacterDesign{60901: fixtureCharacter(6090, 253)})
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
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 99)
	code, response, ok, err := service.Handle("/CashShopPurchaseCountInfo", request)
	if err != nil || !ok || code != 432 || len(response) != 0 {
		t.Fatalf("code=%d bytes=%d ok=%v err=%v", code, len(response), ok, err)
	}
}

func TestTwelvePickSelectionSavePersistsAndReturnsInGachaInfo(t *testing.T) {
	fiveStars := make([]uint64, 12)
	characters := make(map[uint64]gamedata.CharacterDesign, 14)
	for i := range fiveStars {
		fiveStars[i] = uint64(5001 + i)
		characters[fiveStars[i]] = fixtureCharacter(uint64(500+i), 100)
	}
	characters[4001] = fixtureCharacter(400, 100)
	characters[3001] = fixtureCharacter(300, 100)
	design, err := gamedata.NewInfiniteGachaDesignWithRates(10, fiveStars, []uint64{4001}, []uint64{3001}, characters)
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{1: {ID: 1, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 5001, Weight: 1}}}}, characters)
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
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 100)
	for slot, itemID := range fiveStars {
		entry := wire.AppendVarint(nil, 1, paidTwelvePickGroupID)
		if slot != 0 {
			entry = wire.AppendVarint(entry, 2, uint64(slot))
		}
		entry = wire.AppendVarint(entry, 3, itemID)
		request = wire.AppendBytes(request, 2, entry)
	}
	code, response, ok, err := service.Handle("/GachaSelectionSave", request)
	if err != nil || !ok || code != 198 || len(response) != 0 {
		t.Fatalf("save code=%d bytes=%d ok=%v err=%v", code, len(response), ok, err)
	}
	restored, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.GachaSelections(paidTwelvePickGroupID); len(got) != 12 || got[0].ItemID != fiveStars[0] || got[11].Slot != 11 {
		t.Fatalf("restored selections=%+v", got)
	}
	code, response, ok, err = service.Handle("/GachaInfo", wire.AppendVarint(nil, 1, 101))
	if err != nil || !ok || code != 145 || countFields(response, 5) != 12 {
		t.Fatalf("info code=%d selections=%d ok=%v err=%v", code, countFields(response, 5), ok, err)
	}
}

func TestDailyFreeAndPaidSingleDrawsResetByUTCDate(t *testing.T) {
	characters := map[uint64]gamedata.CharacterDesign{5001: fixtureCharacter(500, 100)}
	design, err := gamedata.NewInfiniteGachaDesign(10, []uint64{5001}, characters)
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{
		101: {ID: 101, Count: 1, DailyPayGachaCount: 1, DailyPayGachaPriceCount: 90, FreeCountDay: 1, PriceType: 3, Price: 200, Pool: []gamedata.WeightedCostume{{ID: 5001, Weight: 1}}},
	}, characters)
	if err != nil {
		t.Fatal(err)
	}
	if err := regular.AddGroupDesign(gamedata.GachaGroupDesign{ID: 205, PointCount: 1, OneTimeGachaID: 101}, gamedata.GachaFixedDesign{}); err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{Jewelry: 1000})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	buy := func(seq, buyType uint64) error {
		request := wire.AppendVarint(nil, 1, seq)
		request = wire.AppendVarint(request, 2, 101)
		if buyType != 0 {
			request = wire.AppendVarint(request, 3, buyType)
		}
		code, _, handled, buyErr := service.Handle("/GachaBuy", request)
		if code != 146 || !handled {
			t.Fatalf("buy seq=%d type=%d code=%d handled=%v", seq, buyType, code, handled)
		}
		return buyErr
	}
	if err := buy(1, 0); err != nil {
		t.Fatal(err)
	}
	if err := buy(1, 0); err != nil { // transport retry is idempotent
		t.Fatal(err)
	}
	if err := buy(2, 0); err == nil {
		t.Fatal("second daily free draw was accepted")
	}
	if err := buy(3, 2); err != nil {
		t.Fatal(err)
	}
	if got := wallet.Snapshot().Jewelry; got != 910 {
		t.Fatalf("paid daily jewelry=%d want=910", got)
	}
	if err := buy(4, 2); err == nil {
		t.Fatal("second daily paid draw was accepted")
	}
	_, info, _, err := service.Handle("/GachaInfo", wire.AppendVarint(nil, 1, 5))
	if err != nil {
		t.Fatal(err)
	}
	user, found, err := wire.Bytes(info, 2)
	free, freeFound, freeErr := wire.Varint(user, 4)
	paid, paidFound, paidErr := wire.Varint(user, 5)
	if err != nil || freeErr != nil || paidErr != nil || !found || !freeFound || !paidFound || free != 1 || paid != 1 {
		t.Fatalf("daily info free=%d paid=%d found=%v/%v/%v err=%v/%v/%v", free, paid, found, freeFound, paidFound, err, freeErr, paidErr)
	}
	now = now.Add(24 * time.Hour)
	if err := buy(6, 0); err != nil {
		t.Fatal(err)
	}
	if err := buy(7, 2); err != nil {
		t.Fatal(err)
	}
	if got := wallet.Snapshot().Jewelry; got != 820 {
		t.Fatalf("next-day paid jewelry=%d want=820", got)
	}
}

func TestDailyResetKeyUsesOfficialUTCBoundaryNotHostMidnight(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	before := time.Date(2026, 9, 24, 23, 59, 59, 0, time.UTC)
	after := before.Add(time.Second)
	if before.In(shanghai).Day() != after.In(shanghai).Day() {
		t.Fatal("fixture unexpectedly crosses Shanghai midnight")
	}
	if got := dailyResetKey(before); got != "2026-09-24" {
		t.Fatalf("before key=%q", got)
	}
	if got := dailyResetKey(after); got != "2026-09-25" {
		t.Fatalf("after key=%q", got)
	}
	// Shanghai midnight is 16:00 UTC and must not reset the official bucket.
	localMidnight := time.Date(2026, 9, 25, 0, 0, 0, 0, shanghai)
	if got := dailyResetKey(localMidnight.Add(-time.Second)); got != dailyResetKey(localMidnight) {
		t.Fatalf("host midnight changed official key: %q -> %q", dailyResetKey(localMidnight.Add(-time.Second)), got)
	}
}

func TestMoonriseSelectionCashProductAndOneTimeTicketDraw(t *testing.T) {
	fiveStars := make([]uint64, 12)
	characters := make(map[uint64]gamedata.CharacterDesign, 14)
	choices := make([]gamedata.CostumeRewardEntry, 12)
	for i := range fiveStars {
		fiveStars[i] = uint64(5001 + i)
		characters[fiveStars[i]] = fixtureCharacter(uint64(500+i), 100)
		choices[i] = gamedata.CostumeRewardEntry{ItemType: 11, ItemID: fiveStars[i], Count: 1, Weight: 1}
	}
	characters[4001] = fixtureCharacter(400, 100)
	characters[3001] = fixtureCharacter(300, 100)
	design, err := gamedata.NewInfiniteGachaDesignWithRates(10, fiveStars, []uint64{4001}, []uint64{3001}, characters)
	if err != nil {
		t.Fatal(err)
	}
	moonrise := gamedata.RegularGacha{ID: moonriseProductID, Count: 10, PriceType: moonriseTicketType, PriceID: moonriseTicketID, Price: 1, RewardGroup: &gamedata.CostumeRewardGroup{
		ID: moonriseProductID, DropCount: 1, DropType: 1, Entries: []gamedata.CostumeRewardEntry{
			{ItemType: 9, ItemID: 9100038, Count: 1, Weight: 1, Group: &gamedata.CostumeRewardGroup{ID: 9100038, DropCount: 3, Entries: choices}},
			{ItemType: 9, ItemID: 9100039, Count: 1, Weight: 1, Group: &gamedata.CostumeRewardGroup{ID: 9100039, DropCount: 7, Entries: []gamedata.CostumeRewardEntry{
				{ItemType: 11, ItemID: 4001, Count: 1, Weight: 1443},
				{ItemType: 11, ItemID: 3001, Count: 1, Weight: 8557},
			}}},
		},
	}}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{moonriseProductID: moonrise}, characters)
	if err != nil {
		t.Fatal(err)
	}
	if err := regular.AddGroupDesign(gamedata.GachaGroupDesign{ID: paidTwelvePickGroupID, PointCount: 1, BuyLimitCount: 10, CashProductGroupID: moonriseProductGroupID, CashProductID: moonriseProductID, TenTimeGachaID: moonriseProductID, SelectCount: 12, SelectionChoiceRate: 100, GachaSubType: 1}, gamedata.GachaFixedDesign{}); err != nil {
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
	inventory, err := player.OpenInventory(storage, &player.Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	service.AttachInventory(inventory)
	if err := service.AttachSchedule(&ScheduleSeed{ClientVersion: "test", Schedules: []ScheduleWindow{{GroupID: paidTwelvePickGroupID, StartTime: 1, EndTime: 2}}, StepUps: []ScheduleWindow{{GroupID: 30, StartTime: 1, EndTime: 2}}}); err != nil {
		t.Fatal(err)
	}
	product := wire.AppendVarint(nil, 1, moonriseProductID)
	product = wire.AppendVarint(product, 3, 1)
	buyProduct := wire.AppendVarint(nil, 1, 1)
	buyProduct = wire.AppendVarint(buyProduct, 3, moonriseProductGroupID)
	buyProduct = wire.AppendBytes(buyProduct, 4, product)
	if code, _, handled, err := service.Handle("/CashShopBuy", buyProduct); err != nil || !handled || code != 61 {
		t.Fatalf("cash product code=%d handled=%v err=%v", code, handled, err)
	}
	if !inventory.WasGranted(moonriseTicketGrant) {
		t.Fatal("moonrise cash product purchase was not persisted")
	}
	if _, bought := collection.Grant(moonriseProductGrant); !bought {
		t.Fatal("moonrise cash purchase marker was not persisted")
	}
	selection := wire.AppendVarint(nil, 1, 2)
	for slot, itemID := range fiveStars {
		entry := wire.AppendVarint(nil, 1, paidTwelvePickGroupID)
		if slot != 0 {
			entry = wire.AppendVarint(entry, 2, uint64(slot))
		}
		entry = wire.AppendVarint(entry, 3, itemID)
		selection = wire.AppendBytes(selection, 2, entry)
	}
	if _, _, _, err := service.Handle("/GachaSelectionSave", selection); err != nil {
		t.Fatal(err)
	}
	draw := wire.AppendVarint(nil, 1, 3)
	draw = wire.AppendVarint(draw, 2, moonriseProductID)
	draw = wire.AppendVarint(draw, 3, 3)
	if code, response, handled, err := service.Handle("/GachaBuy", draw); err != nil || !handled || code != 146 || countFields(response, 4) != 3 {
		t.Fatalf("moonrise draw code=%d handled=%v err=%v", code, handled, err)
	}
	grant, found := collection.Grant(moonriseDrawGrant)
	if !found || len(grant.ViewCostumeIDs) != 0 {
		// The completion grant is deliberately an empty marker; the concrete
		// result remains keyed by the request identity for retry replay.
		if !found {
			t.Fatal("moonrise completion marker missing")
		}
	}
	if got := collection.GachaUser(paidTwelvePickGroupID).TotalBuyCount; got != 10 {
		t.Fatalf("moonrise total buy count=%d want=10", got)
	}
	if _, _, _, err := service.Handle("/GachaBuy", wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 4), 2, moonriseProductID), 3, 3)); err == nil {
		t.Fatal("second moonrise draw was accepted")
	}
	_, info, _, err := service.Handle("/GachaInfo", wire.AppendVarint(nil, 1, 5))
	if err != nil || len(collectScheduleWindows(t, info, 1)) != 0 || !sameScheduleGroups(collectScheduleWindows(t, info, 7), 30) {
		t.Fatalf("post-moonrise schedules=%v step=%v err=%v", scheduleGroupIDs(collectScheduleWindows(t, info, 1)), scheduleGroupIDs(collectScheduleWindows(t, info, 7)), err)
	}
	counts := service.PurchaseCountDBInfos()
	if len(counts) != 1 {
		t.Fatalf("purchase counts=%d want=1", len(counts))
	}
	group, _, _ := wire.Varint(counts[0], 1)
	id, _, _ := wire.Varint(counts[0], 2)
	if group != moonriseProductGroupID || id != moonriseProductID {
		t.Fatalf("purchase count group=%d id=%d", group, id)
	}
}

func TestSelectionChangeCountPersistsEnforcesLimitAndReturnsInGachaInfo(t *testing.T) {
	fiveStars := []uint64{5001, 5002}
	characters := map[uint64]gamedata.CharacterDesign{
		5001: fixtureCharacter(500, 100),
		5002: fixtureCharacter(501, 100),
		4001: fixtureCharacter(400, 100),
		3001: fixtureCharacter(300, 100),
	}
	design, err := gamedata.NewInfiniteGachaDesignWithRates(10, fiveStars, []uint64{4001}, []uint64{3001}, characters)
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{
		101: {ID: 101, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 5001, Weight: 1}}},
	}, characters)
	if err != nil {
		t.Fatal(err)
	}
	const groupID = 7001
	if err := regular.AddGroupDesign(gamedata.GachaGroupDesign{
		ID: groupID, PointCount: 1, TenTimeGachaID: 101, SelectCount: 1, SelectionChangeCount: 2,
	}, gamedata.GachaFixedDesign{}); err != nil {
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
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	save := func(seq, itemID uint64) error {
		request := wire.AppendVarint(nil, 1, seq)
		entry := wire.AppendVarint(nil, 1, groupID)
		entry = wire.AppendVarint(entry, 3, itemID)
		request = wire.AppendBytes(request, 2, entry)
		code, response, handled, saveErr := service.Handle("/GachaSelectionSave", request)
		if code != 198 || !handled || len(response) != 0 {
			t.Fatalf("save code=%d handled=%v response=%x", code, handled, response)
		}
		return saveErr
	}
	if err := save(1, 5001); err != nil {
		t.Fatal(err)
	}
	// Re-saving the exact same slots is not a selection change and must not
	// consume the finite GameData allowance.
	if err := save(2, 5001); err != nil {
		t.Fatal(err)
	}
	if err := save(3, 5002); err != nil {
		t.Fatal(err)
	}
	if err := save(4, 5001); err == nil {
		t.Fatal("selection save exceeded GameData change limit")
	}

	_, info, handled, err := service.Handle("/GachaInfo", wire.AppendVarint(nil, 1, 5))
	if err != nil || !handled || countFields(info, 6) != 1 {
		t.Fatalf("info change fields=%d handled=%v err=%v", countFields(info, 6), handled, err)
	}
	change, found, err := wire.Bytes(info, 6)
	gotGroup, groupFound, groupErr := wire.Varint(change, 1)
	gotCount, countFound, countErr := wire.Varint(change, 2)
	if err != nil || groupErr != nil || countErr != nil || !found || !groupFound || !countFound || gotGroup != groupID || gotCount != 2 {
		t.Fatalf("change group=%d count=%d found=%v/%v/%v err=%v/%v/%v", gotGroup, gotCount, found, groupFound, countFound, err, groupErr, countErr)
	}
	restored, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	counts := restored.GachaSelectionChangeCounts()
	if len(counts) != 1 || counts[0].GroupID != groupID || counts[0].Count != 2 {
		t.Fatalf("restored change counts=%+v", counts)
	}
}

func TestOrdinaryTwelvePickGacha101UsesSavedSelectionGroupAndPersists(t *testing.T) {
	fiveStars := make([]uint64, 12)
	characters := make(map[uint64]gamedata.CharacterDesign, 14)
	for i := range fiveStars {
		fiveStars[i] = uint64(5001 + i)
		characters[fiveStars[i]] = fixtureCharacter(uint64(500+i), 100)
	}
	characters[4001] = fixtureCharacter(400, 100)
	characters[3001] = fixtureCharacter(300, 100)
	design, err := gamedata.NewInfiniteGachaDesignWithRates(10, fiveStars, []uint64{4001}, []uint64{3001}, characters)
	if err != nil {
		t.Fatal(err)
	}
	pool := []gamedata.WeightedCostume{
		{Weight: 300, Children: []gamedata.WeightedCostume{{ID: 5001, Weight: 1}}},
		{Weight: 1400, Children: []gamedata.WeightedCostume{{ID: 4001, Weight: 1}}},
		{Weight: 8300, Children: []gamedata.WeightedCostume{{ID: 3001, Weight: 1}}},
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{
		101: {ID: 101, Count: 10, PriceType: 3, Price: 2000, Pool: pool, TicketIDs: []uint64{1108, 1000}},
	}, characters)
	if err != nil {
		t.Fatal(err)
	}
	if err := regular.AddGroupDesign(gamedata.GachaGroupDesign{ID: twelvePickGroupID, FixedID: 8, PointCount: 1, TenTimeGachaID: 101, SelectCount: 12, SelectionChoiceRate: 100, GachaSubType: 1, IsSelectedFromPity: true}, gamedata.GachaFixedDesign{ID: 8, CostumeGrade4Count: 10, CostumeGrade5Count: 100, ResetOnMatchingGrade: true}); err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{FreeJewelry: 2000})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	service.BeginSession("ordinary-12pick")
	save := wire.AppendVarint(nil, 1, 1)
	for slot, itemID := range fiveStars {
		choice := wire.AppendVarint(nil, 1, twelvePickGroupID)
		if slot != 0 {
			choice = wire.AppendVarint(choice, 2, uint64(slot))
		}
		choice = wire.AppendVarint(choice, 3, itemID)
		save = wire.AppendBytes(save, 2, choice)
	}
	if _, _, _, err := service.Handle("/GachaSelectionSave", save); err != nil {
		t.Fatal(err)
	}
	buy := wire.AppendVarint(nil, 1, 2)
	buy = wire.AppendVarint(buy, 2, 101)
	buy = wire.AppendVarint(buy, 3, 1)
	code, response, ok, err := service.Handle("/GachaBuy", buy)
	if err != nil || !ok || code != 146 {
		t.Fatalf("buy code=%d ok=%v err=%v", code, ok, err)
	}
	bundle, found, err := wire.Bytes(response, 1)
	if err != nil || !found || countFields(bundle, 3)+countFields(bundle, 9) != 10 {
		t.Fatalf("bundle=%x found=%v err=%v", bundle, found, err)
	}
	if user := collection.GachaUser(twelvePickGroupID); user.Point != 10 || user.TotalBuyCount != 10 {
		t.Fatalf("user=%+v", user)
	}
	fixed := collection.GachaFixedStates()
	if len(fixed) != 2 || fixed[0].FixedID != 8 || fixed[1].FixedID != 8 {
		t.Fatalf("fixed=%+v", fixed)
	}
	if wallet.Snapshot().FreeJewelry != 0 {
		t.Fatalf("wallet=%+v", wallet.Snapshot())
	}
}

func countFields(data []byte, number int) int {
	count := 0
	_ = wire.Walk(data, func(field wire.Field) error {
		if field.Number == number {
			count++
		}
		return nil
	})
	return count
}
