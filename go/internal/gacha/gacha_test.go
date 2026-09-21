package gacha

import (
	"path/filepath"
	"testing"

	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/wire"
)

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
	path := filepath.Join(t.TempDir(), "collection.json")
	collection, err := player.OpenCollectionStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{1: {ID: 1, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 60901, Weight: 1}}}}, map[uint64]gamedata.CharacterDesign{60901: fixtureCharacter(6090, 253)})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(filepath.Join(t.TempDir(), "wallet.json"), player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
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
	lock := wire.AppendVarint(nil, 1, 2)
	lock = wire.AppendVarint(lock, 2, 30010)
	code, response, ok, err = service.Handle("/GachaBuyPreviewLock", lock)
	if err != nil || !ok || code != 175 || len(response) != 0 {
		t.Fatalf("preview lock code=%d bytes=%d ok=%v err=%v", code, len(response), ok, err)
	}
	if eventIndex, locked := collection.PreviewLock(); !locked || eventIndex != 30010 {
		t.Fatalf("preview lock event=%d locked=%v", eventIndex, locked)
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
	restored, err := player.OpenCollectionStore(path, nil)
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
	if eventIndex, locked := restored.PreviewLock(); !locked || eventIndex != 30010 {
		t.Fatalf("restored preview lock event=%d locked=%v", eventIndex, locked)
	}
	code, response, ok, err = service.Handle("/GachaInfo", wire.AppendVarint(nil, 1, 4))
	if err != nil || !ok || code != 145 || countFields(response, 1) != 3 {
		t.Fatalf("post-purchase info code=%d schedules=%d ok=%v err=%v", code, countFields(response, 1), ok, err)
	}
	code, response, ok, err = service.Handle("/CashShopPurchaseCountInfo", wire.AppendVarint(nil, 1, 5))
	countInfo, found, parseErr := wire.Bytes(response, 1)
	count, countFound, countErr := wire.Varint(countInfo, 4)
	if err != nil || parseErr != nil || countErr != nil || !ok || code != 432 || !found || !countFound || count != 1 {
		t.Fatalf("purchase count code=%d count=%d found=%v/%v ok=%v err=%v/%v/%v", code, count, found, countFound, ok, err, parseErr, countErr)
	}
	if _, _, _, err := service.Handle("/GachaBuyPreview", preview); err == nil {
		t.Fatal("purchased infinite gacha accepted a new preview")
	}
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
	dir := t.TempDir()
	collection, err := player.OpenCollectionStore(filepath.Join(dir, "collection.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(filepath.Join(dir, "wallet.json"), player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	preview := wire.AppendVarint(nil, 1, 1)
	preview = wire.AppendVarint(preview, 2, gamedata.InfiniteGachaID)
	preview = wire.AppendVarint(preview, 3, gamedata.InfiniteProductGroupID)
	preview = wire.AppendVarint(preview, 4, gamedata.InfiniteProductID)
	if _, _, _, err := service.Handle("/GachaBuyPreview", preview); err != nil {
		t.Fatal(err)
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
	lock = wire.AppendVarint(lock, 2, 30010)
	if _, _, _, err := service.Handle("/GachaBuyPreviewLock", lock); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Handle("/GachaBuyPreview", preview); err != nil {
		t.Fatal(err)
	}
	if eventIndex, locked := collection.PreviewLock(); locked || eventIndex != 0 {
		t.Fatalf("reroll kept stale lock event=%d locked=%v", eventIndex, locked)
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
	dir := t.TempDir()
	collection, err := player.OpenCollectionStore(filepath.Join(dir, "collection.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(filepath.Join(dir, "wallet.json"), player.Currency{FreeJewelry: 500})
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
	dir := t.TempDir()
	collection, err := player.OpenCollectionStore(filepath.Join(dir, "collection.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(filepath.Join(dir, "wallet.json"), player.Currency{FreeJewelry: 400})
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
	dir := t.TempDir()
	collection, err := player.OpenCollectionStore(filepath.Join(dir, "collection.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(filepath.Join(dir, "wallet.json"), player.Currency{Jewelry: 5000})
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

func TestCompletedStepUpPersistsAndRemainsVisible(t *testing.T) {
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
	dir := t.TempDir()
	collectionPath := filepath.Join(dir, "collection.json")
	collection, err := player.OpenCollectionStore(collectionPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(filepath.Join(dir, "wallet.json"), player.Currency{Jewelry: 1000})
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
	restored, err := player.OpenCollectionStore(collectionPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewService(design, regular, restored, wallet)
	if err != nil {
		t.Fatal(err)
	}
	restarted.BeginSession("second-login")
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
	dir := t.TempDir()
	collection, err := player.OpenCollectionStore(filepath.Join(dir, "collection.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(filepath.Join(dir, "wallet.json"), player.Currency{FreeJewelry: 2000})
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
	dir := t.TempDir()
	collectionPath := filepath.Join(dir, "collection.json")
	walletPath := filepath.Join(dir, "wallet.json")
	collection, err := player.OpenCollectionStore(collectionPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(walletPath, player.Currency{FreeJewelry: 2000})
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
	restored, err := player.OpenCollectionStore(collectionPath, nil)
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
	dir := t.TempDir()
	collection, err := player.OpenCollectionStore(filepath.Join(dir, "collection.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(filepath.Join(dir, "wallet.json"), player.Currency{})
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
	dir := t.TempDir()
	collectionPath := filepath.Join(dir, "collection.json")
	collection, err := player.OpenCollectionStore(collectionPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(filepath.Join(dir, "wallet.json"), player.Currency{})
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
	restored, err := player.OpenCollectionStore(collectionPath, nil)
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
	dir := t.TempDir()
	collection, err := player.OpenCollectionStore(filepath.Join(dir, "collection.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(filepath.Join(dir, "wallet.json"), player.Currency{FreeJewelry: 2000})
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
