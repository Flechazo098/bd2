package commerce

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func nativeBuyRequest(seq, group uint64, lines ...[3]uint64) []byte {
	b := wire.AppendVarint(nil, 1, seq)
	b = wire.AppendVarint(b, 3, group)
	for _, line := range lines {
		row := wire.AppendVarint(nil, 1, line[0])
		row = wire.AppendVarint(row, 2, line[1])
		row = wire.AppendVarint(row, 3, line[2])
		b = wire.AppendBytes(b, 4, row)
	}
	// Native clients repeat these display fields on subsequent purchases.
	return wire.AppendBytes(b, 7, wire.AppendString(nil, 1, "2"))
}

func TestNativeGoodsSingleQuantityFreeClaimsAndDisplayBilling(t *testing.T) {
	s, eco, _ := serviceFixture(t, 0)
	key := gamedata.CashProductKey{GroupID: 44, ProductID: 2}
	d := gamedata.CashProductDesign{Key: key, PriceType: 22, PriceCount: 10, RandomBoxID: 500, PurchaseLimitType: 3, PurchaseLimitCount: 301}
	s.catalog.designs[key] = d
	req := nativeBuyRequest(1, 44, [3]uint64{2, 0, 300})
	code, out, handled, err := s.HandleSession("/CashShopBuy", req, "s")
	if err != nil || !handled || code != 61 || eco.calls != 1 {
		t.Fatal(code, handled, err)
	}
	if eco.costs[0] != (gamedata.Reward{Type: 22, Count: 3000}) || eco.rewards[0].Count != 300 {
		t.Fatal(eco)
	}
	_, replay, _, err := s.HandleSession("/CashShopBuy", req, "s")
	if err != nil || !bytes.Equal(out, replay) || eco.calls != 1 {
		t.Fatal("duplicate native request", err)
	}
	if _, _, _, err = s.HandleSession("/CashShopBuy", nativeBuyRequest(2, 44, [3]uint64{2, 0, 1}), "s"); err != nil {
		t.Fatal("display billing treated as receipt", err)
	}
	if _, _, _, err = s.HandleSession("/CashShopBuy", nativeBuyRequest(3, 44, [3]uint64{2, 0, 1}), "s"); err == nil || eco.calls != 2 {
		t.Fatal("monthly limit ignored")
	}
	freeKey := gamedata.CashProductKey{GroupID: 45, ProductID: 1}
	s.catalog.designs[freeKey] = gamedata.CashProductDesign{Key: freeKey, RandomBoxID: 501, PurchaseLimitType: 4, PurchaseLimitCount: 1}
	if _, _, _, err = s.HandleSession("/CashShopBuy", nativeBuyRequest(4, 45, [3]uint64{1, 0, 1}), "s"); err != nil || len(eco.costs) != 0 {
		t.Fatal("free claim failed", err)
	}
	if _, _, _, err = s.HandleSession("/CashShopBuy", nativeBuyRequest(5, 45, [3]uint64{1, 0, 1}), "s"); err == nil {
		t.Fatal("free claimed twice")
	}
}

func TestNativeMultipleProductsAndSpecialPreviewsAreValidatedBeforeGrant(t *testing.T) {
	s, eco, _ := serviceFixture(t, 0)
	key := gamedata.CashProductKey{GroupID: 44, ProductID: 1}
	other := gamedata.CashProductKey{GroupID: 44, ProductID: 2}
	s.catalog.designs[key] = gamedata.CashProductDesign{Key: key, PriceType: 4, PriceCount: 1, RandomBoxID: 1}
	s.catalog.designs[other] = gamedata.CashProductDesign{Key: other, PriceType: 4, PriceCount: 1, RandomBoxID: 2, BulkOrderAvailability: 1}
	if _, _, _, err := s.HandleSession("/CashShopBuy", nativeBuyRequest(1, 44, [3]uint64{1, 0, 1}, [3]uint64{2, 0, 1}), "s"); err == nil || eco.calls != 0 {
		t.Fatal("ineligible batch executed")
	}
	if err := s.AttachSpecialProducts([]gamedata.CashProductKey{key}); err != nil {
		t.Fatal(err)
	}
	delegates := 0
	s.AttachDelegate(func(k gamedata.CashProductKey, _ []byte) ([]byte, bool, error) {
		delegates++
		if k != key {
			return nil, false, fmt.Errorf("unexpected delegate key")
		}
		return []byte{8, 1}, true, nil
	})
	if _, _, _, err := s.HandleSession("/CashShopBuy", nativeBuyRequest(2, 44, [3]uint64{1, 0, 2}), "s"); err == nil || delegates != 0 || eco.calls != 0 {
		t.Fatal("invalid preview mutated state")
	}
	if _, _, _, err := s.HandleSession("/CashShopBuy", nativeBuyRequest(3, 44, [3]uint64{1, 0, 1}), "s"); err != nil || delegates != 1 || len(eco.rewards) != 0 {
		t.Fatal("preview did not use delegate", err)
	}
	if _, _, _, err := s.HandleSession("/CashShopBuy", nativeBuyRequest(4, 44, [3]uint64{2, 0, 2}), "s"); err != nil || delegates != 1 {
		t.Fatal("ordinary native delegated", err)
	}
}

type nativeTestGraph struct{}

func (nativeTestGraph) Resolve(rewards []gamedata.BattleReward) ([]gamedata.BattleReward, error) {
	var out []gamedata.BattleReward
	for _, r := range rewards {
		out = append(out, gamedata.BattleReward{Type: 8, ID: 710, Count: r.Count})
	}
	return out, nil
}

func TestNativeTotalDiamondPurchaseSQLiteRollbackAndRestartReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(repo, player.Currency{FreeJewelry: 5, Jewelry: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err = wallet.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	fixture, _, _ := serviceFixture(t, 0)
	key := gamedata.CashProductKey{GroupID: 44, ProductID: 2}
	fixture.catalog.designs[key] = gamedata.CashProductDesign{Key: key, PriceType: 3, PriceCount: 7, RandomBoxID: 1}
	open := func() (*Service, *player.Inventory) {
		t.Helper()
		items, err := player.OpenInventory(repo, &player.Starter{Version: "2.35.10"})
		if err != nil {
			t.Fatal(err)
		}
		collection, err := player.OpenCollectionStore(repo, nil)
		if err != nil {
			t.Fatal(err)
		}
		equipment, err := player.OpenEquipmentInventory(repo)
		if err != nil {
			t.Fatal(err)
		}
		economy, err := events.NewEconomy(repo, items, wallet, collection, equipment, &gamedata.RegularGachaCatalog{}, &gamedata.EquipmentGachaCatalog{}, nativeTestGraph{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		s, err := NewService(fixture.catalog, repo, economy)
		if err != nil {
			t.Fatal(err)
		}
		return s, items
	}
	s, _ := open()
	s.AttachPurchaseHook(func(string, gamedata.CashProductDesign, uint64) error { return fmt.Errorf("grant failed") })
	req := nativeBuyRequest(1, 44, [3]uint64{2, 0, 1})
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.HandleSession("/CashShopBuy", req, "s"); err == nil {
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
	s, items := open()
	if wallet.Snapshot().FreeJewelry != 5 || wallet.Snapshot().Jewelry != 10 || len(items.All()) != 0 || s.HasPurchased(key) {
		t.Fatal("rollback retained partial purchase")
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	_, out, _, err := s.HandleSession("/CashShopBuy", req, "s")
	if err != nil {
		t.Fatal(err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().FreeJewelry != 0 || wallet.Snapshot().Jewelry != 8 || len(items.All()) != 1 {
		t.Fatal("total diamonds not spent free-first")
	}
	s, items = open()
	_, replay, _, err := s.HandleSession("/CashShopBuy", req, "s")
	if err != nil || !bytes.Equal(out, replay) || wallet.Snapshot().Jewelry != 8 || len(items.All()) != 1 {
		t.Fatal("restart replay repeated debit/grant", err)
	}
}
