package commerce

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gameconfig"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/readonly"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

type purchaseEconomy struct {
	calls          int
	costs, rewards []gamedata.Reward
	fail           bool
}

func (e *purchaseEconomy) Apply(_ string, c, r []gamedata.Reward) ([]byte, error) {
	e.calls++
	e.costs = c
	e.rewards = r
	if e.fail {
		return nil, fmt.Errorf("insufficient balance")
	}
	return []byte{8, 1}, nil
}
func serviceFixture(t *testing.T, limit uint64) (*Service, *purchaseEconomy, *stateio.Memory) {
	t.Helper()
	design := &gamedata.CashCatalog{Products: []gamedata.CashProductDesign{
		{Key: gamedata.CashProductKey{GroupID: 1, ProductID: 1}, GoogleSKU: "topup", PriceType: 1, PriceCount: 2200, RandomBoxID: 10, Recharge: true, NominalPaidDiamonds: 100},
		{Key: gamedata.CashProductKey{GroupID: 2, ProductID: 1}, GoogleSKU: "goods", PriceType: 1, PriceCount: 22000, RandomBoxID: 20, PurchaseLimitType: limit, PurchaseLimitCount: 1},
		{Key: gamedata.CashProductKey{GroupID: 2, ProductID: 1, SaleGroup: 1}, GoogleSKU: "goods-sale", PriceType: 1, PriceCount: 11000, RandomBoxID: 21, PurchaseLimitType: 4, PurchaseLimitCount: 1},
	}}
	c, err := NewCatalog("test", design, gameconfig.Default().Purchases)
	if err != nil {
		t.Fatal(err)
	}
	eco := &purchaseEconomy{}
	store := stateio.NewMemory()
	s, err := NewService(c, store, eco)
	if err != nil {
		t.Fatal(err)
	}
	return s, eco, store
}
func buyRequest(seq, group, id, sale uint64, billing string) []byte {
	b := wire.AppendVarint(nil, 1, seq)
	b = wire.AppendVarint(b, 3, group)
	line := wire.AppendVarint(nil, 1, id)
	line = wire.AppendVarint(line, 2, sale)
	line = wire.AppendVarint(line, 3, 1)
	b = wire.AppendBytes(b, 4, line)
	if billing == "" {
		billing = fmt.Sprint(seq)
	}
	if billing == "pay-1" {
		billing = "1"
	}
	typ, amount := uint64(2), uint64(1000)
	if sale == 1 {
		amount = 500
	}
	if group == 1 {
		typ = 0
		amount = 0
	}
	info := wire.AppendBytes(nil, 1, []byte(billing))
	info = wire.AppendBytes(info, 2, []byte(fmt.Sprintf("bd2-local-commerce-v1:%s:%d:%d", billing, typ, amount)))
	b = wire.AppendBytes(b, 7, info)
	return b
}
func TestPurchaseReplayPersistsAndRejectsCollisions(t *testing.T) {
	s, eco, store := serviceFixture(t, 0)
	req := buyRequest(1, 2, 1, 0, "pay-1")
	code, out, ok, err := s.HandleSession("/CashShopBuy", req, "session")
	if err != nil || !ok || code != 61 {
		t.Fatal(code, ok, err)
	}
	if eco.calls != 1 || len(eco.costs) != 1 || eco.costs[0].Type != 2 || eco.costs[0].Count != 1000 || eco.rewards[0].ID != 20 {
		t.Fatal(eco)
	}
	s2, err := NewService(s.catalog, store, eco)
	if err != nil {
		t.Fatal(err)
	}
	_, replayed, _, err := s2.HandleSession("/CashShopBuy", req, "session")
	if err != nil || !bytes.Equal(out, replayed) || eco.calls != 1 {
		t.Fatal(err, eco.calls)
	}
	changed := buyRequest(1, 2, 1, 1, "")
	if _, _, _, err = s2.HandleSession("/CashShopBuy", changed, "session"); err == nil {
		t.Fatal("changed sequence accepted")
	}
	if _, _, _, err = s2.HandleSession("/CashShopBuy", req, "other-session"); err == nil {
		t.Fatal("billing identity reused")
	}
}
func TestSaleGroupsLimitsAndReset(t *testing.T) {
	s, eco, _ := serviceFixture(t, 1)
	now := time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now }, 0)
	if _, _, _, err := s.HandleSession("/CashShopBuy", buyRequest(1, 2, 1, 0, ""), "s"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.HandleSession("/CashShopBuy", buyRequest(2, 2, 1, 0, ""), "s"); err == nil {
		t.Fatal("daily limit ignored")
	}
	if _, _, _, err := s.HandleSession("/CashShopBuy", buyRequest(3, 2, 1, 1, ""), "s"); err != nil {
		t.Fatal("sale group should be independent", err)
	}
	now = now.Add(24 * time.Hour)
	if _, _, _, err := s.HandleSession("/CashShopBuy", buyRequest(4, 2, 1, 0, ""), "s"); err != nil {
		t.Fatal(err)
	}
	if eco.calls != 3 {
		t.Fatal(eco.calls)
	}
}
func TestFailedEconomyDoesNotConsumeLimitAndFreeRecharge(t *testing.T) {
	s, eco, _ := serviceFixture(t, 4)
	eco.fail = true
	req := buyRequest(1, 2, 1, 0, "")
	if _, _, _, err := s.HandleSession("/CashShopBuy", req, "s"); err == nil {
		t.Fatal("failure accepted")
	}
	if s.HasPurchased(gamedata.CashProductKey{GroupID: 2, ProductID: 1}) {
		t.Fatal("failed buy persisted")
	}
	eco.fail = false
	if _, _, _, err := s.HandleSession("/CashShopBuy", req, "s"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.HandleSession("/CashShopBuy", buyRequest(2, 1, 1, 0, ""), "s"); err != nil {
		t.Fatal(err)
	}
	if len(eco.costs) != 0 || eco.rewards[0].ID != 10 {
		t.Fatal("free recharge wrong", eco)
	}
}
func TestAuthenticatedSessionRequired(t *testing.T) {
	s, eco, _ := serviceFixture(t, 0)
	if _, _, _, err := s.Handle("/CashShopBuy", buyRequest(1, 2, 1, 0, "")); err == nil || eco.calls != 0 {
		t.Fatal("unauthenticated buy accepted")
	}
}
func TestPassEntitlementConsumedOncePerPurchase(t *testing.T) {
	s, _, _ := serviceFixture(t, 0)
	k := gamedata.CashProductKey{GroupID: 2, ProductID: 1}
	if s.ConsumeEntitlement(k) {
		t.Fatal("unpaid entitlement authorized")
	}
	for i := uint64(1); i <= 2; i++ {
		if _, _, _, err := s.HandleSession("/CashShopBuy", buyRequest(i, 2, 1, 0, ""), "s"); err != nil {
			t.Fatal(err)
		}
		if !s.ConsumeEntitlement(k) || s.ConsumeEntitlement(k) {
			t.Fatal("entitlement count not enforced")
		}
	}
}
func TestRelayRequiresPreviousProduct(t *testing.T) {
	s, eco, _ := serviceFixture(t, 0)
	if err := s.AttachPackageRules([]gamedata.CashPackageDesign{{GroupID: 2, ID: 1, PackageType: 8, ContentsGroupID: 7, ContentsSortID: 1}, {GroupID: 2, ID: 1, SaleGroup: 1, PackageType: 8, ContentsGroupID: 7, ContentsSortID: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.HandleSession("/CashShopBuy", buyRequest(1, 2, 1, 1, ""), "s"); err == nil || eco.calls != 0 {
		t.Fatal("relay step skipped")
	}
	if _, _, _, err := s.HandleSession("/CashShopBuy", buyRequest(2, 2, 1, 0, ""), "s"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.HandleSession("/CashShopBuy", buyRequest(3, 2, 1, 1, ""), "s"); err != nil {
		t.Fatal(err)
	}
}

func TestStaleClientQuoteRejectedBeforeEconomy(t *testing.T) {
	s, eco, _ := serviceFixture(t, 0)
	req := buyRequest(1, 2, 1, 0, "")
	req = bytes.ReplaceAll(req, []byte(":2:1000"), []byte(":3:1000"))
	if _, _, _, err := s.HandleSession("/CashShopBuy", req, "s"); err == nil || eco.calls != 0 {
		t.Fatal("stale currency accepted")
	}
	req = buyRequest(2, 2, 1, 0, "")
	req = bytes.ReplaceAll(req, []byte(":2:1000"), []byte(":2:9999"))
	if _, _, _, err := s.HandleSession("/CashShopBuy", req, "s"); err == nil || eco.calls != 0 {
		t.Fatal("stale amount accepted")
	}
}
func TestShopPublishesAllWindowsAndAuthorizesActiveProducts(t *testing.T) {
	s, eco, _ := serviceFixture(t, 0)
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now }, 0)
	seed := &readonly.Seed{Responses: map[string]readonly.Response{"/CashShopInfo": {PacketCode: 60, Fields: []readonly.Field{{Number: 1, Type: 2, Fields: []readonly.Field{{Number: 1, Type: 0, Varint: 2}, {Number: 2, Type: 0, Varint: 1}, {Number: 4, Type: 0, Varint: uint64(now.Add(-time.Hour).UnixMilli())}, {Number: 5, Type: 0, Varint: uint64(now.Add(time.Hour).UnixMilli())}, {Number: 8, Type: 0, Varint: 123}}}}}}}
	if err := s.AttachShopSeed(seed); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.HandleSession("/CashShopBuy", buyRequest(1, 2, 1, 0, ""), "s"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if _, _, _, err := s.HandleSession("/CashShopBuy", buyRequest(2, 2, 1, 0, ""), "s"); err == nil {
		t.Fatal("expired product accepted")
	}
	code, out, ok, err := s.HandleSession("/CashShopInfo", wire.AppendVarint(nil, 1, 1), "s")
	if err != nil || !ok || code != 60 {
		t.Fatal(err)
	}
	products := 0
	_ = wire.Walk(out, func(f wire.Field) error {
		if f.Number == 1 {
			products++
		}
		return nil
	})
	if products != 1 || eco.calls != 1 {
		t.Fatal(products, eco.calls)
	}
}

type walletPurchaseEconomy struct {
	wallet *player.Wallet
	store  stateio.Store
}

func (e walletPurchaseEconomy) Apply(identity string, costs, rewards []gamedata.Reward) ([]byte, error) {
	if err := e.wallet.ExchangeOnce(identity, costs, nil); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(rewards)
	if err != nil {
		return nil, err
	}
	if err = e.store.Save("commerce_test_rewards", raw); err != nil {
		return nil, err
	}
	return []byte{8, 1}, nil
}
func TestSQLiteAccountTransactionRollsBackCostRewardAndReceipt(t *testing.T) {
	s, _, _ := serviceFixture(t, 4)
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	wallet, err := player.OpenWallet(repo, player.Currency{Jewelry: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if err = wallet.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	s, err = NewService(s.catalog, repo, walletPurchaseEconomy{wallet, repo})
	if err != nil {
		t.Fatal(err)
	}
	s.AttachPurchaseHook(func(string, gamedata.CashProductDesign, uint64) error { return fmt.Errorf("entitlement failure") })
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.HandleSession("/CashShopBuy", buyRequest(1, 2, 1, 0, ""), "s"); err == nil {
		t.Fatal("hook failure lost")
	}
	if err = op.Rollback(); err != nil && !errors.Is(err, stateio.ErrStateRecoveryRequired) {
		t.Fatal(err)
	}
	if err = repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	wallet, err = player.OpenWallet(repo, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().Jewelry != 1000 {
		t.Fatal("paid diamonds survived rollback")
	}
	for _, name := range []string{"commerce", "commerce_test_rewards"} {
		raw, err := repo.Load(name)
		if err != nil || raw != nil {
			t.Fatal("purchase state survived rollback", name, err)
		}
	}
	s, err = NewService(s.catalog, repo, walletPurchaseEconomy{wallet, repo})
	if err != nil {
		t.Fatal(err)
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.HandleSession("/CashShopBuy", buyRequest(1, 2, 1, 0, ""), "s"); err != nil {
		_ = op.Rollback()
		t.Fatal(err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().Jewelry != 0 || !s.HasPurchased(gamedata.CashProductKey{GroupID: 2, ProductID: 1}) {
		t.Fatal("retry did not commit")
	}
}
