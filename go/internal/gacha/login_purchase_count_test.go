package gacha

import (
	"path/filepath"
	"testing"

	"bd2server/internal/account"
	"bd2server/internal/accountstate"
	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/wire"
)

func TestLoginPurchaseCountsRestoredFromSQLiteGrant(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	repository, err := accountstate.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}

	design, err := gamedata.NewInfiniteGachaDesign(10, []uint64{60901}, map[uint64]gamedata.CharacterDesign{
		60901: {ID: 6090, HP: 253, CostumeMaxLevel: 5, OverflowItemType: 20, OverflowItemCount: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(
		map[uint64]gamedata.RegularGacha{
			1: {ID: 1, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 60901, Weight: 1}}},
		},
		map[uint64]gamedata.CharacterDesign{
			60901: {ID: 6090, HP: 253, CostumeMaxLevel: 5, OverflowItemType: 20, OverflowItemCount: 2},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := player.OpenCollectionStore(repository, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(repository, player.Currency{})
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
	lock := wire.AppendVarint(nil, 1, 2)
	lock = wire.AppendVarint(lock, 2, 1171)
	if _, _, _, err := service.Handle("/GachaBuyPreviewLock", lock); err != nil {
		t.Fatal(err)
	}
	buy := wire.AppendVarint(nil, 1, 3)
	buy = wire.AppendVarint(buy, 3, gamedata.InfiniteProductGroupID)
	product := wire.AppendVarint(nil, 1, gamedata.InfiniteProductID)
	product = wire.AppendVarint(product, 3, 1)
	buy = wire.AppendBytes(buy, 4, product)
	if _, _, _, err := service.Handle("/CashShopBuy", buy); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen the database and construct fresh stores/services so this assertion
	// cannot pass from an in-memory grant left by the purchase call above.
	repository, err = accountstate.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	collection, err = player.OpenCollectionStore(repository, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := collection.Grant(infiniteGrant); !found {
		t.Fatal("SQLite-backed infinite cash-product grant was not restored")
	}
	wallet, err = player.OpenWallet(repository, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err = NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}

	stale := wire.AppendVarint(nil, 1, 999)
	userTemplate := wire.AppendVarint(nil, 1, 42)
	userTemplate = wire.AppendBytes(userTemplate, 26, stale)
	loginSeed := &account.LoginSeed{Version: account.ProtocolVersion(), PacketCode: 11, UserInfo: userTemplate}
	if err := loginSeed.AttachPurchaseCounts(service); err != nil {
		t.Fatal(err)
	}
	loginResponse, err := loginSeed.Login(wire.AppendVarint(nil, 1, 4), []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	user, found, err := wire.Bytes(loginResponse, 1)
	if err != nil || !found {
		t.Fatalf("LoginUser missing UserDBInfo: found=%v err=%v", found, err)
	}
	loginCounts := purchaseCountFields(user, 26)
	if len(loginCounts) != 1 {
		t.Fatalf("LoginUser purchase counts=%d, want 1", len(loginCounts))
	}
	assertInfinitePurchaseCount(t, loginCounts[0])

	code, rpcResponse, handled, err := service.Handle("/CashShopPurchaseCountInfo", wire.AppendVarint(nil, 1, 5))
	if err != nil || !handled || code != 432 {
		t.Fatalf("purchase-count RPC code=%d handled=%v err=%v", code, handled, err)
	}
	rpcCounts := purchaseCountFields(rpcResponse, 1)
	if len(rpcCounts) != 1 || string(rpcCounts[0]) != string(loginCounts[0]) {
		t.Fatalf("LoginUser/RPC purchase counts differ: login=%x rpc=%x", loginCounts, rpcCounts)
	}
}

func assertInfinitePurchaseCount(t *testing.T, data []byte) {
	t.Helper()
	group, groupFound, groupErr := wire.Varint(data, 1)
	id, idFound, idErr := wire.Varint(data, 2)
	saleGroup, saleGroupFound, saleGroupErr := wire.Varint(data, 3)
	count, countFound, countErr := wire.Varint(data, 4)
	if groupErr != nil || idErr != nil || saleGroupErr != nil || countErr != nil ||
		!groupFound || !idFound || saleGroupFound || !countFound ||
		group != gamedata.InfiniteProductGroupID || id != gamedata.InfiniteProductID || saleGroup != 0 || count != 1 {
		t.Fatalf("PurchaseCountDBInfo group=%d id=%d saleGroup=%d count=%d found=%v/%v/%v/%v errors=%v/%v/%v/%v",
			group, id, saleGroup, count, groupFound, idFound, saleGroupFound, countFound,
			groupErr, idErr, saleGroupErr, countErr)
	}
}

func purchaseCountFields(data []byte, number int) [][]byte {
	var result [][]byte
	if err := wire.Walk(data, func(field wire.Field) error {
		if field.Number == number && field.Type == 2 {
			result = append(result, append([]byte(nil), field.Value...))
		}
		return nil
	}); err != nil {
		return nil
	}
	return result
}
