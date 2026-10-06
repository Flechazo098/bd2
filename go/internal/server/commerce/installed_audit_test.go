package commerce

import (
	"bd2server/internal/server/calendar"
	"bd2server/internal/server/events"
	"bd2server/internal/server/gameconfig"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/hunting"
	"bd2server/internal/server/mail"
	"bd2server/internal/server/player"
	"bd2server/internal/server/readonly"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestInstalledCashProductRewardCoverage23510(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	catalog, err := gamedata.LoadCashCatalog(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := gamedata.LoadCashRewardResolver(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	entitlements, err := gamedata.LoadCashEntitlementDesign(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	// Infinite draw products grant the user's confirmed preview, rather than
	// the placeholder cash box. Validate their dedicated design and route.
	db, release, err := gamedata.OpenDatabase(root, "20260923193640", "common")
	if err != nil {
		t.Fatal(err)
	}
	rowsDB, err := db.Query("SELECT id,ProtoBuf FROM GachaGroupTable")
	if err != nil {
		release()
		t.Fatal(err)
	}
	type drawGroup struct{ id, gacha uint64 }
	var groups []drawGroup
	for rowsDB.Next() {
		var id uint64
		var raw []byte
		if err := rowsDB.Scan(&id, &raw); err != nil {
			t.Fatal(err)
		}
		typ, _, _ := wire.Varint(raw, 17)
		sub, _, _ := wire.Varint(raw, 16)
		if typ == 1 && sub == 5 {
			gacha, _, _ := wire.Varint(raw, 33)
			groups = append(groups, drawGroup{id, gacha})
		}
	}
	if err := rowsDB.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rowsDB.Close(); err != nil {
		t.Fatal(err)
	}
	release()
	policy, err := NewCatalog("2.35.10", catalog, gameconfig.Default().Purchases)
	if err != nil {
		t.Fatal(err)
	}
	shop, err := NewService(policy, stateio.NewMemory(), &purchaseEconomy{})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := readonly.Load(filepath.Join("..", "..", "..", "seed", "v2_35_10", "readonly.json"))
	if err != nil {
		t.Fatal(err)
	}
	calendars, err := calendar.LoadDirectory(filepath.Join("..", "..", "..", "..", "schedules"), "2.35.10", "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	seed, err = calendars.ApplyReadonly(seed)
	if err != nil {
		t.Fatal(err)
	}
	shop.SetClock(func() time.Time { return time.Date(2026, 10, 5, 5, 0, 0, 0, time.UTC) }, 0)
	if err := shop.AttachShopSeed(seed); err != nil {
		t.Fatal(err)
	}
	if err := shop.AttachEventShopSchedules(catalog, calendars.Events); err != nil {
		t.Fatal(err)
	}
	graph, err := gamedata.LoadRewardGraph(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	costumes, err := gamedata.LoadRewardCostumeCatalog(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	equipment, err := gamedata.LoadRewardEquipmentCatalog(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	skins, err := gamedata.LoadPrestigeSkins(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	ownedDesign, err := gamedata.LoadOwnedEventItemDesign(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	avatarRewards, err := gamedata.LoadAvatarRewardDesign(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	templates, err := gamedata.LoadCashMailTemplates(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	newEconomy := func() (*EntitlementEconomy, *player.Wallet) {
		store := stateio.NewMemory()
		items, err := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
		if err != nil {
			t.Fatal(err)
		}
		wallet, err := player.OpenWallet(store, player.Currency{Jewelry: 100000000, FreeJewelry: 100000000, Gold: 100000000})
		if err != nil {
			t.Fatal(err)
		}
		collection, err := player.OpenCollectionStore(store, nil)
		if err != nil {
			t.Fatal(err)
		}
		ownedEquipment, err := player.OpenEquipmentInventory(store)
		if err != nil {
			t.Fatal(err)
		}
		for _, persist := range []func() error{items.EnsurePersisted, wallet.EnsurePersisted, collection.EnsurePersisted, ownedEquipment.EnsurePersisted} {
			if err := persist(); err != nil {
				t.Fatal(err)
			}
		}
		base, err := events.NewEconomy(store, items, wallet, collection, ownedEquipment, costumes, equipment, graph, nil)
		if err != nil {
			t.Fatal(err)
		}
		base.AttachPrestigeSkins(skins)
		base.AttachOwnedItemDesign(ownedDesign)
		base.AttachAvatarRewards(avatarRewards)
		ap, err := hunting.Open(store, root, "20260923193640", items, wallet, func() (int, error) { return 21, nil }, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		base.AttachHuntingAP(ap)
		e, err := NewEntitlementEconomy(store, base, resolver, items, entitlements)
		if err != nil {
			t.Fatal(err)
		}
		e.SetClock(func() time.Time { return time.Date(2026, 10, 5, 5, 0, 0, 0, time.UTC) }, 0)
		mailbox, err := mail.OpenService(store, &mail.Starter{Version: "2.35.10", MailCount: 1}, items, wallet)
		if err != nil {
			t.Fatal(err)
		}
		if err = mailbox.AttachCashRewards(e, templates); err != nil {
			t.Fatal(err)
		}
		if err = e.AttachCashMail(mailbox); err != nil {
			t.Fatal(err)
		}
		return e, wallet
	}
	t.Run("event shop complete calendar and native purchase", func(t *testing.T) {
		e, wallet := newEconomy()
		shop, err := NewService(policy, e.store, e)
		if err != nil {
			t.Fatal(err)
		}
		if err = shop.AttachShopSeed(seed); err != nil {
			t.Fatal(err)
		}
		rows := append([]events.Schedule(nil), calendars.Events...)
		var current events.Schedule
		var lastEnd int64
		for _, row := range rows {
			if row.Type == 15 && row.ID == 71 {
				current = row
				if row.End > lastEnd {
					lastEnd = row.End
				}
			}
		}
		if current.UID == 0 {
			t.Fatal("real event shop 71 calendar missing")
		}
		future := events.Schedule{UID: 99999999, Type: 15, ID: 71, Start: lastEnd + 86400000, End: lastEnd + 2*86400000}
		rows = append(rows, future)
		if err = shop.AttachEventShopSchedules(catalog, rows); err != nil {
			t.Fatal(err)
		}
		shop.SetClock(func() time.Time { return time.UnixMilli(current.Start + 1) }, 0)
		_, info, handled, err := shop.HandleSession("/CashShopInfo", wire.AppendVarint(nil, 1, 1), "audit")
		if err != nil || !handled {
			t.Fatal(err)
		}
		actual := map[uint64]map[gamedata.CashProductKey]bool{}
		_ = wire.Walk(info, func(f wire.Field) error {
			if f.Number != 1 || f.Type != 2 {
				return nil
			}
			uid, _, _ := wire.Varint(f.Value, 8)
			if uid == 0 {
				return nil
			}
			group, _, _ := wire.Varint(f.Value, 1)
			id, _, _ := wire.Varint(f.Value, 2)
			sale, _, _ := wire.Varint(f.Value, 3)
			if actual[uid] == nil {
				actual[uid] = map[gamedata.CashProductKey]bool{}
			}
			actual[uid][gamedata.CashProductKey{GroupID: group, ProductID: id, SaleGroup: sale}] = true
			return nil
		})
		seenOld := false
		for _, row := range rows {
			if row.Type != 15 {
				continue
			}
			group := uint64(0)
			for _, design := range catalog.EventShops {
				if design.ID == row.ID {
					group = design.ProductGroupID
				}
			}
			if row.ID == 70 {
				seenOld = true
			}
			if row.ID == 71 && group != 900071 {
				t.Fatal("event shop 71 product group mismatch")
			}
			expected := 0
			for _, product := range catalog.Products {
				if product.Key.GroupID == group {
					expected++
					if !actual[row.UID][product.Key] {
						t.Fatalf("shop %d UID %d missing product %+v", row.ID, row.UID, product.Key)
					}
				}
			}
			if len(actual[row.UID]) != expected {
				t.Fatalf("shop %d UID %d count=%d expected=%d", row.ID, row.UID, len(actual[row.UID]), expected)
			}
		}
		if !seenOld || len(actual[future.UID]) == 0 {
			t.Fatal("old or future event shop filtered")
		}
		fund := []gamedata.Reward{{Type: 8, ID: 2074, Count: 100}}
		if _, err = e.Apply("audit funds", nil, fund); err != nil {
			t.Fatal(err)
		}
		beforeWallet := wallet.Snapshot()
		countItem := func(id uint64) uint64 {
			var n uint64
			for _, item := range e.items.All() {
				if item.Type == 8 && item.ID == id {
					n += item.Count
				}
			}
			return n
		}
		beforeCoin, beforeReward := countItem(2074), countItem(710)
		request := wire.AppendVarint(nil, 1, 2)
		request = wire.AppendVarint(request, 3, 900071)
		for _, id := range []uint64{1, 5} {
			line := wire.AppendVarint(nil, 1, id)
			line = wire.AppendVarint(line, 3, 1)
			request = wire.AppendBytes(request, 4, line)
		}
		code, response, handled, err := shop.HandleSession("/CashShopBuy", request, "audit")
		if err != nil || !handled || code != 61 {
			t.Fatalf("native batch code=%d handled=%t err=%v", code, handled, err)
		}
		if wallet.Snapshot().FreeJewelry != beforeWallet.FreeJewelry-100 || countItem(2074) != beforeCoin-12 || countItem(710) != beforeReward+1 {
			t.Fatal("native batch debit or real reward missing", wallet.Snapshot(), e.items.All())
		}
		for _, id := range []uint64{1, 5} {
			found := false
			for _, raw := range shop.PurchaseCountDBInfos() {
				g, _, _ := wire.Varint(raw, 1)
				p, _, _ := wire.Varint(raw, 2)
				n, _, _ := wire.Varint(raw, 4)
				if g == 900071 && p == id && n == 1 {
					found = true
				}
			}
			if !found {
				t.Fatal("native purchase count missing", id)
			}
		}
		balance := wallet.Snapshot()
		coins := countItem(2074)
		rewardCount := countItem(710)
		_, replay, _, err := shop.HandleSession("/CashShopBuy", request, "audit")
		if err != nil || !bytes.Equal(response, replay) || wallet.Snapshot() != balance || countItem(2074) != coins || countItem(710) != rewardCount {
			t.Fatal("native purchase replay duplicated", err)
		}
	})
	delegated := map[gamedata.CashProductKey]bool{}
	retired := map[gamedata.CashProductKey]bool{}
	for _, group := range groups {
		d, err := gamedata.LoadInfiniteGachaForSchedules(root, "20260923193640", []uint64{group.id})
		if err != nil && strings.Contains(err.Error(), "infinite cash product missing") {
			// Early infinite draws retain placeholder gold/count=0 boxes in this
			// version. They are historical timed products, not grantable purchases.
			found := false
			for _, p := range catalog.Products {
				if p.Key.ProductID != group.gacha {
					continue
				}
				found = true
				if p.TimeLimitType == 0 || shop.IsAvailable(p.Key) {
					t.Fatalf("retired infinite product %+v is purchasable without a valid preview design", p.Key)
				}
				retired[p.Key] = true
			}
			if !found {
				t.Fatalf("infinite group=%d has no matching retired product", group.id)
			}
			continue
		}
		if err != nil || d == nil || d.Count == 0 {
			t.Fatalf("infinite draw group=%d cannot grant confirmed preview: %v", group.id, err)
		}
		delegated[gamedata.CashProductKey{GroupID: d.ProductGroupID, ProductID: d.ProductID, SaleGroup: d.SaleGroup}] = true
	}
	cash, recharge, delegateCount, retiredCount, mailedProducts := 0, 0, 0, 0, 0
	for _, p := range catalog.Products {
		if p.PriceType != 1 {
			continue
		}
		cash++
		if p.Recharge {
			recharge++
		}
		if delegated[p.Key] {
			delegateCount++
			continue
		}
		if retired[p.Key] {
			retiredCount++
			continue
		}
		if p.RandomBoxID == 0 {
			t.Errorf("cash product %+v has no reward box", p.Key)
			continue
		}
		for _, box := range []uint64{p.RandomBoxID, p.BonusRandomBoxID} {
			if box == 0 {
				continue
			}
			leaves, err := resolver.ResolveGranted([]gamedata.BattleReward{{Type: 9, ID: box, Count: 1}})
			if err != nil || len(leaves) == 0 {
				t.Errorf("cash product %+v box=%d cannot grant rewards: %v", p.Key, box, err)
				continue
			}
			for _, r := range leaves {
				if r.Type == 19 && entitlements.TicketTypes[r.ID] == 0 || r.Type == 62 && !entitlements.AvatarSets[r.ID] {
					t.Errorf("cash product %+v has unknown entitlement %+v", p.Key, r)
				}
			}
		}
		// Exercise the same grant and debit modules used by CashShopBuy against
		// isolated in-memory accounts; no real save or authenticated API is touched.
		q, err := policy.Quote(p.Key, 1)
		if err != nil {
			t.Fatal(err)
		}
		var costs []gamedata.Reward
		if q.Cost > 0 {
			costs = []gamedata.Reward{{Type: q.ItemType, Count: q.Cost}}
		}
		rewards := []gamedata.Reward{{Type: 9, ID: p.RandomBoxID, Count: 1}}
		if p.BonusRandomBoxID != 0 {
			rewards = append(rewards, gamedata.Reward{Type: 9, ID: p.BonusRandomBoxID, Count: 1})
		}
		e, wallet := newEconomy()
		bundle, err := e.ApplyPurchase("installed-audit", costs, rewards)
		if err != nil {
			t.Errorf("cash product %+v failed real debit/grant: %v", p.Key, err)
			continue
		}
		_ = wire.Walk(bundle, func(f wire.Field) error {
			if f.Number == 1 && f.Type == 2 {
				typ, _, _ := wire.Varint(f.Value, 3)
				if typ == 62 {
					t.Errorf("cash product %+v returned synthetic avatar set", p.Key)
				}
			}
			return nil
		})
		balance := wallet.Snapshot()
		replay, err := e.ApplyPurchase("installed-audit", costs, rewards)
		if err != nil || !bytes.Equal(replay, bundle) || wallet.Snapshot() != balance {
			t.Errorf("cash product %+v replay repeated debit/grant: %v", p.Key, err)
		}

		mailbox := e.mail.(*mail.Service)
		listReq := wire.AppendVarint(nil, 1, 1)
		listReq = wire.AppendVarint(listReq, 3, 100)
		code, list, ok, err := mailbox.Handle("/CashMailInfo", listReq)
		if err != nil || !ok || code != 140 {
			t.Fatalf("cash list %+v: %v", p.Key, err)
		}
		claim := wire.AppendVarint(nil, 1, 2)
		mailCount := 0
		_ = wire.Walk(list, func(f wire.Field) error {
			if f.Number == 1 && f.Type == 2 {
				id, _, _ := wire.Varint(f.Value, 1)
				template, _, _ := wire.Varint(f.Value, 3)
				cash, _, _ := wire.Varint(f.Value, 15)
				if !templates[template] || cash != 1 {
					t.Errorf("invalid cash mail template/flag %+v", p.Key)
				}
				claim = wire.AppendVarint(claim, 2, id)
				mailCount++
			}
			return nil
		})
		if mailCount > 0 {
			mailedProducts++
			_, claimed, _, err := mailbox.Handle("/MailOpen", claim)
			if err != nil {
				t.Errorf("cash claim %+v: %v", p.Key, err)
				continue
			}
			claimedBalance := wallet.Snapshot()
			claimedItems := e.items.All()
			_, replayed, _, err := mailbox.Handle("/MailOpen", claim)
			if err != nil || !bytes.Equal(claimed, replayed) || wallet.Snapshot() != claimedBalance || !reflect.DeepEqual(e.items.All(), claimedItems) {
				t.Errorf("cash claim replay %+v duplicated: %v", p.Key, err)
			}
			_, remaining, _, err := mailbox.Handle("/CashMailInfo", listReq)
			n, _, _ := wire.Varint(remaining, 2)
			if err != nil || n != 0 {
				t.Errorf("claimed cash mail remains %+v", p.Key)
			}
		}
		// Cash mailbox delivery must grant exactly what the previous immediate
		// reward resolver granted, including subscriptions' first-day deduplication.
		reference, referenceWallet := newEconomy()
		if _, err := reference.Apply("reference", costs, rewards); err != nil {
			t.Fatal(err)
		}
		itemTotals := func(items []player.Item) map[[3]uint64]uint64 {
			totals := map[[3]uint64]uint64{}
			for _, item := range items {
				totals[[3]uint64{item.Type, item.ID, item.ExpiryTime}] += item.Count
			}
			return totals
		}
		if wallet.Snapshot() != referenceWallet.Snapshot() || !reflect.DeepEqual(itemTotals(e.items.All()), itemTotals(reference.items.All())) {
			t.Errorf("cash product %+v mailed delivery differs from direct grant: wallet=%+v expected=%+v items=%+v expected=%+v", p.Key, wallet.Snapshot(), referenceWallet.Snapshot(), e.items.All(), reference.items.All())
		}
	}
	if cash != 614 || recharge != 14 || delegateCount != 3 || retiredCount != 5 {
		t.Fatalf("cash coverage changed: cash=%d recharge=%d delegated=%d retired=%d", cash, recharge, delegateCount, retiredCount)
	}
	bonus, err := gamedata.LoadCashBonusCatalog(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	rows := bonus.Groups[1]
	if len(rows) != 3 || rows[0].RequireCount != 2 || rows[0].Reward.Count != 2 || rows[1].RequireCount != 6 || rows[1].Reward.Count != 15 || rows[2].RequireCount != 8 || rows[2].Reward.Count != 30 {
		t.Fatalf("cash bonus thresholds or rewards changed: %+v", rows)
	}
	t.Logf("validated %d cash rows: %d recharge variants, %d preview designs, %d unavailable historical draws; checked %d products through cash mail creation, batch claim, and replay with typed ticket/skin entitlements", cash, recharge, delegateCount, retiredCount, mailedProducts)
}
