package commerce

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func TestEventShopPublishesAllSchedulesAndNativePurchaseReplay(t *testing.T) {
	s, eco, store := serviceFixture(t, 0)
	key := gamedata.CashProductKey{GroupID: 700, ProductID: 3}
	d := gamedata.CashProductDesign{Key: key, PriceType: 8, PriceID: 99, PriceCount: 5, RandomBoxID: 20, PurchaseLimitType: 4, PurchaseLimitCount: 2, BulkOrderAvailability: 1}
	s.catalog.designs[key] = d
	s.shopWindows = map[gamedata.CashProductKey][2]uint64{}
	design := &gamedata.CashCatalog{EventShops: []gamedata.EventShopDesign{{ID: 7, ProductGroupID: 700}}}
	rows := []events.Schedule{{UID: 9, Type: 15, ID: 7, Start: 100, End: 200}}
	if err := s.AttachEventShopSchedules(design, rows); err != nil {
		t.Fatal(err)
	}
	for _, now := range []int64{50, 150, 250} {
		s.SetClock(func() time.Time { return time.UnixMilli(now) }, 0)
		_, out, _, err := s.HandleSession("/CashShopInfo", wire.AppendVarint(nil, 1, 1), "s")
		raw, found, _ := wire.Bytes(out, 1)
		group, _, _ := wire.Varint(raw, 1)
		uid, _, _ := wire.Varint(raw, 8)
		if err != nil || !found || group != 700 || uid != 9 {
			t.Fatalf("calendar filtered at %d: %v", now, err)
		}
		if s.IsAvailable(key) != (now == 150) {
			t.Fatal("window authorization differs")
		}
	}
	req := wire.AppendVarint(nil, 1, 1)
	req = wire.AppendVarint(req, 3, 700)
	line := wire.AppendVarint(nil, 1, 3)
	line = wire.AppendVarint(line, 3, 2)
	req = wire.AppendBytes(req, 4, line)
	if _, _, handled, err := s.HandleSession("/CashShopBuy", req, "s"); !handled || err == nil || eco.calls != 0 {
		t.Fatal("expired purchase authorized")
	}
	s.SetClock(func() time.Time { return time.UnixMilli(150) }, 0)
	_, out, handled, err := s.HandleSession("/CashShopBuy", req, "s")
	if err != nil || !handled || eco.calls != 1 {
		t.Fatal(err, handled, eco.calls)
	}
	if len(eco.costs) != 1 || eco.costs[0] != (gamedata.Reward{Type: 8, ID: 99, Count: 10}) || eco.rewards[0].Count != 2 {
		t.Fatal(eco)
	}
	// Reload service state, then replay the same request without another grant.
	s2, err := NewService(s.catalog, store, eco)
	if err != nil {
		t.Fatal(err)
	}
	s2.shopWindows = map[gamedata.CashProductKey][2]uint64{}
	if err = s2.AttachEventShopSchedules(design, rows); err != nil {
		t.Fatal(err)
	}
	s2.SetClock(func() time.Time { return time.UnixMilli(250) }, 0)
	_, replay, _, err := s2.HandleSession("/CashShopBuy", req, "s")
	if err != nil || !bytes.Equal(out, replay) || eco.calls != 1 {
		t.Fatal("replay failed", err)
	}
	req[1] = 2
	if _, _, _, err = s.HandleSession("/CashShopBuy", req, "s"); err == nil || eco.calls != 1 {
		t.Fatal("purchase limit ignored")
	}
}

func TestNativeEventShopSQLiteRollsBackDebitGrantAndPurchaseCount(t *testing.T) {
	fixture, _, _ := serviceFixture(t, 0)
	key := gamedata.CashProductKey{GroupID: 700, ProductID: 3}
	fixture.catalog.designs[key] = gamedata.CashProductDesign{Key: key, PriceType: 3, PriceCount: 5, RandomBoxID: 20, PurchaseLimitType: 4, PurchaseLimitCount: 1}
	design := &gamedata.CashCatalog{EventShops: []gamedata.EventShopDesign{{ID: 7, ProductGroupID: 700}}}
	rows := []events.Schedule{{UID: 9, Type: 15, ID: 7, Start: 100, End: 200}}
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(repo, player.Currency{FreeJewelry: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err = wallet.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	open := func() *Service {
		t.Helper()
		s, err := NewService(fixture.catalog, repo, walletPurchaseEconomy{wallet, repo})
		if err != nil {
			t.Fatal(err)
		}
		s.shopWindows = map[gamedata.CashProductKey][2]uint64{}
		if err = s.AttachEventShopSchedules(design, rows); err != nil {
			t.Fatal(err)
		}
		s.SetClock(func() time.Time { return time.UnixMilli(150) }, 0)
		return s
	}
	s := open()
	s.AttachPurchaseHook(func(string, gamedata.CashProductDesign, uint64) error { return fmt.Errorf("grant hook failed") })
	req := wire.AppendVarint(nil, 1, 1)
	req = wire.AppendVarint(req, 3, 700)
	line := wire.AppendVarint(nil, 1, 3)
	line = wire.AppendVarint(line, 3, 1)
	req = wire.AppendBytes(req, 4, line)
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, handled, err := s.HandleSession("/CashShopBuy", req, "s"); !handled || err == nil {
		t.Fatal("hook failure swallowed")
	}
	if err = op.Rollback(); err != nil && !errors.Is(err, stateio.ErrStateRecoveryRequired) {
		t.Fatal(err)
	}
	repo.Close()
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	wallet, err = player.OpenWallet(repo, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().FreeJewelry != 10 {
		t.Fatal("debit survived rollback")
	}
	for _, name := range []string{"commerce", "commerce_test_rewards"} {
		raw, err := repo.Load(name)
		if err != nil || raw != nil {
			t.Fatal("grant or receipt survived rollback", name, err)
		}
	}
	s = open()
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.HandleSession("/CashShopBuy", req, "s"); err != nil {
		t.Fatal(err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().FreeJewelry != 5 || !s.HasPurchased(key) {
		t.Fatal("retry did not persist")
	}
}
