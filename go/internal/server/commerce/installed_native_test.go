package commerce

import (
	"bd2server/internal/server/gameconfig"
	"bd2server/internal/server/readonly"
	"bd2server/internal/server/wire"
	"bytes"
	"fmt"
	"os"
	"testing"
	"time"

	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/hunting"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
)

// Covers static native goods independently of their current publication
// windows. The account is an isolated memory store; no player saves are read.
func TestInstalledAllNativeProductRewardAndPriceDomains(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	const version = "20260923193640"
	catalog, err := gamedata.LoadCashCatalog(root, version)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := gamedata.LoadRewardGraph(root, version)
	if err != nil {
		t.Fatal(err)
	}
	costumes, err := gamedata.LoadRewardCostumeCatalog(root, version)
	if err != nil {
		t.Fatal(err)
	}
	options, err := gamedata.LoadRewardEquipmentCatalog(root, version)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := gamedata.LoadOwnedEventItemDesign(root, version)
	if err != nil {
		t.Fatal(err)
	}
	avatars, err := gamedata.LoadAvatarRewardDesign(root, version)
	if err != nil {
		t.Fatal(err)
	}
	skins, err := gamedata.LoadPrestigeSkins(root, version)
	if err != nil {
		t.Fatal(err)
	}
	newAccount := func() (*events.Economy, *player.Inventory, *player.Wallet, stateio.Store, *hunting.Service) {
		store := stateio.NewMemory()
		items, err := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
		if err != nil {
			t.Fatal(err)
		}
		wallet, err := player.OpenWallet(store, player.Currency{})
		if err != nil {
			t.Fatal(err)
		}
		collection, err := player.OpenCollectionStore(store, nil)
		if err != nil {
			t.Fatal(err)
		}
		equipment, err := player.OpenEquipmentInventory(store)
		if err != nil {
			t.Fatal(err)
		}
		economy, err := events.NewEconomy(store, items, wallet, collection, equipment, costumes, options, graph, nil)
		if err != nil {
			t.Fatal(err)
		}
		economy.AttachOwnedItemDesign(owned)
		economy.AttachAvatarRewards(avatars)
		economy.AttachPrestigeSkins(skins)
		ap, err := hunting.Open(store, root, version, items, wallet, func() (int, error) { return 21, nil }, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		economy.AttachHuntingAP(ap)
		return economy, items, wallet, store, ap
	}
	economy, _, _, _, _ := newAccount()
	families := map[uint64]int{}
	free := 0
	native := 0
	// Each product reward is settled once through the actual shared economy.
	// Prices are funded in that same typed domain and spent once separately,
	// avoiding historical publication or predecessor constraints in this audit.
	for _, product := range catalog.Products {
		if product.PriceType == 1 {
			continue
		}
		native++
		if native%50 == 0 {
			economy, _, _, _, _ = newAccount()
		}
		families[product.PriceType]++
		identity := fmt.Sprintf("native-audit:%d:%d:%d", product.Key.GroupID, product.Key.ProductID, product.Key.SaleGroup)
		var cost []gamedata.Reward
		if product.PriceType == 0 {
			free++
			if product.PriceID != 0 || product.PriceCount != 0 {
				t.Errorf("free product has nonzero price %+v", product.Key)
			}
		} else {
			cost = []gamedata.Reward{{Type: product.PriceType, ID: product.PriceID, Count: product.PriceCount}}
			if _, err = economy.Apply(identity+":fund", nil, cost); err != nil {
				t.Errorf("native price domain %+v failed: %v", product.Key, err)
				continue
			}
		}
		rewards := []gamedata.Reward{{Type: 9, ID: product.RandomBoxID, Count: 1}}
		if product.BonusRandomBoxID != 0 {
			rewards = append(rewards, gamedata.Reward{Type: 9, ID: product.BonusRandomBoxID, Count: 1})
		}
		if _, err = economy.Apply(identity, cost, rewards); err != nil {
			t.Errorf("native product %+v real reward/debit failed: %v", product.Key, err)
		}
	}
	if native != 2274 || free != 100 || len(families) != 14 {
		t.Fatalf("native design coverage changed: goods=%d free=%d families=%v", native, free, families)
	}
	t.Run("native dispatcher quantity free and total diamonds", func(t *testing.T) {
		base, items, wallet, store, ap := newAccount()
		entitlementDesign, err := gamedata.LoadCashEntitlementDesign(root, version)
		if err != nil {
			t.Fatal(err)
		}
		resolver, err := gamedata.LoadCashRewardResolver(root, version, graph)
		if err != nil {
			t.Fatal(err)
		}
		entitlement, err := NewEntitlementEconomy(store, base, resolver, items, entitlementDesign)
		if err != nil {
			t.Fatal(err)
		}
		policy, err := NewCatalog("2.35.10", catalog, gameconfig.Default().Purchases)
		if err != nil {
			t.Fatal(err)
		}
		service, err := NewService(policy, store, entitlement)
		if err != nil {
			t.Fatal(err)
		}
		field := func(n int, v uint64) readonly.Field { return readonly.Field{Number: n, Type: 0, Varint: v} }
		seed := &readonly.Seed{Responses: map[string]readonly.Response{"/CashShopInfo": {Fields: []readonly.Field{{Number: 1, Type: 2, Fields: []readonly.Field{field(1, 1700001), field(2, 1), field(3, 0), field(4, 1), field(5, uint64(time.Now().Add(time.Hour).UnixMilli()))}}}}}}
		if err = service.AttachShopSeed(seed); err != nil {
			t.Fatal(err)
		}
		if _, err = base.Apply("fund diamonds", nil, []gamedata.Reward{{Type: 3, Count: 1000}, {Type: 2, Count: 50000}}); err != nil {
			t.Fatal(err)
		}
		makeBuy := func(seq, group, id, count uint64) []byte {
			r := wire.AppendVarint(nil, 1, seq)
			r = wire.AppendVarint(r, 3, group)
			line := wire.AppendVarint(nil, 1, id)
			line = wire.AppendVarint(line, 3, count)
			return wire.AppendBytes(r, 4, line)
		}
		request := makeBuy(1, 400001, 2, 300)
		_, bonusBefore, err := ap.HuntingAP()
		if err != nil {
			t.Fatal(err)
		}
		code, response, handled, err := service.HandleSession("/CashShopBuy", request, "native-audit")
		if err != nil || !handled || code != 61 {
			t.Fatalf("native quantity300 code%d handled%t err%v", code, handled, err)
		}
		balance := wallet.Snapshot()
		_, bonus, err := ap.HuntingAP()
		if err != nil || balance.FreeJewelry != 0 || balance.Jewelry != 0 || bonus != bonusBefore+18000 {
			t.Fatal("total diamond split or AP grant wrong", balance, bonus, err)
		}
		_, replay, _, err := service.HandleSession("/CashShopBuy", request, "native-audit")
		_, againBonus, _ := ap.HuntingAP()
		if err != nil || !bytes.Equal(response, replay) || againBonus != bonus || wallet.Snapshot() != balance {
			t.Fatal("bulk native replay duplicated", err)
		}
		found := false
		for _, raw := range service.PurchaseCountDBInfos() {
			g, _, _ := wire.Varint(raw, 1)
			id, _, _ := wire.Varint(raw, 2)
			count, _, _ := wire.Varint(raw, 4)
			if g == 400001 && id == 2 && count == 300 {
				found = true
			}
		}
		if !found {
			t.Fatal("quantity300 count missing")
		}
		free := makeBuy(2, 1700001, 1, 1)
		if code, _, handled, err = service.HandleSession("/CashShopBuy", free, "native-audit"); err != nil || !handled || code != 61 {
			t.Fatal("free native goods failed", err)
		}
		if wallet.Snapshot().Jewelry != balance.Jewelry || wallet.Snapshot().FreeJewelry != balance.FreeJewelry {
			t.Fatal("free goods charged currency")
		}
		if _, _, _, err = service.HandleSession("/CashShopBuy", makeBuy(3, 1700001, 1, 1), "native-audit"); err == nil {
			t.Fatal("free limit bypassed")
		}
		if _, err = base.Apply("fund resource", nil, []gamedata.Reward{{Type: 8, ID: 1002, Count: 3000}}); err != nil {
			t.Fatal(err)
		}
		if code, _, handled, err = service.HandleSession("/CashShopBuy", makeBuy(4, 700001, 2, 300), "native-audit"); err != nil || !handled || code != 61 {
			t.Fatal("native resource quantity300 failed", err)
		}
		for _, item := range items.All() {
			if item.Type == 8 && item.ID == 1002 && item.Count != 0 {
				t.Fatal("resource price not consumed", item)
			}
		}
	})
	t.Logf("validated %d native goods across %d typed price domains, including %d free goods", native, len(families), free)
}
