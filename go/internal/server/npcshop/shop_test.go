package npcshop

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

type economy struct {
	calls          int
	costs, rewards []gamedata.Reward
	fail           bool
}
type failedReceiptStore struct{ stateio.Store }

func (s failedReceiptStore) Save(name string, b []byte) error {
	if name == "npc_shop" {
		return fmt.Errorf("receipt disk failure")
	}
	return s.Store.Save(name, b)
}

type storedEconomy struct{ store stateio.Store }

func (e storedEconomy) Apply(_ string, _ []gamedata.Reward, r []gamedata.Reward) ([]byte, error) {
	if err := e.store.Save("npc_test_grants", []byte{1}); err != nil {
		return nil, err
	}
	return []byte{8, 1}, nil
}
func TestFailedSaleReceiptRollsBackConcreteInventoryAndReward(t *testing.T) {
	base, _, _ := fixture(t)
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	starter := &player.Starter{Version: versionconfig.State()}
	items, err := player.OpenInventory(repo, starter)
	if err != nil {
		t.Fatal(err)
	}
	granted, err := items.GrantOnce("initial-food", []gamedata.BattleReward{{Type: 5, ID: 400, Count: 8}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(base.design, failedReceiptStore{repo}, storedEconomy{repo}, items, base.available)
	if err != nil {
		t.Fatal(err)
	}
	s.now = base.now
	s.SetSessionSource(base.session)
	line := wire.AppendVarint(nil, 1, granted[0].InvenIndex)
	line = wire.AppendVarint(line, 2, 19)
	line = wire.AppendVarint(line, 3, 3)
	line = wire.AppendVarint(line, 4, 100)
	req := wire.AppendBytes(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 71), 3, line)
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.Handle("/ShopSell", req); err == nil {
		t.Fatal("expected failed receipt persistence")
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
	reopened, err := player.OpenInventory(repo, starter)
	if err != nil {
		t.Fatal(err)
	}
	if rows := reopened.All(); len(rows) != 1 || rows[0].Count != 8 {
		t.Fatal("inventory removal survived rollback", rows)
	}
	for _, name := range []string{"npc_shop", "npc_test_grants"} {
		raw, err := repo.Load(name)
		if err != nil || raw != nil {
			t.Fatal("reward or receipt survived rollback", name, err)
		}
	}
}

func (e *economy) Apply(_ string, c, r []gamedata.Reward) ([]byte, error) {
	if e.fail {
		return nil, fmt.Errorf("insufficient balance")
	}
	e.calls++
	e.costs = c
	e.rewards = r
	return []byte{8, 1}, nil
}
func fixture(t *testing.T) (*Service, *economy, *stateio.Memory) {
	t.Helper()
	store := stateio.NewMemory()
	items, err := player.OpenInventory(store, &player.Starter{Version: versionconfig.State()})
	if err != nil {
		t.Fatal(err)
	}
	d := gamedata.NPCShopDesign{Shops: map[uint64]gamedata.NPCShop{71: {ID: 71, PackID: 91, ResetType: 1}}, Products: map[uint64]map[uint64]gamedata.NPCProduct{71: {3: {ID: 3, GroupID: 71, MaxCount: 3, Reward: gamedata.Reward{Type: 5, ID: 400, Count: 2}, Price: gamedata.Reward{Type: 4, Count: 25}}}}, Sell: map[uint64]gamedata.NPCProduct{19: {ID: 19, Reward: gamedata.Reward{Type: 5, ID: 400, Count: 1}, Price: gamedata.Reward{Type: 4, Count: 7}}}}
	e := &economy{}
	s, err := New(d, store, e, items, func(pack uint64) bool { return pack == 91 })
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) }
	s.SetSessionSource(func() string { return "login" })
	return s, e, store
}
func buy(seq, n, rate uint64) []byte {
	b := wire.AppendVarint(nil, 1, seq)
	g := wire.AppendVarint(nil, 1, 71)
	p := wire.AppendVarint(nil, 2, 3)
	p = wire.AppendVarint(p, 3, n)
	p = wire.AppendVarint(p, 4, rate)
	return wire.AppendBytes(b, 2, wire.AppendBytes(g, 2, p))
}
func TestPurchaseReceiptLimitsResetAndWholeBatchValidation(t *testing.T) {
	s, e, store := fixture(t)
	request := buy(1, 2, 100)
	code, response, ok, err := s.Handle("/ShopBuy", request)
	if err != nil || !ok || code != 54 {
		t.Fatal(code, ok, err)
	}
	if e.calls != 1 || len(e.costs) != 1 || e.costs[0].Count != 50 || e.rewards[0].Count != 4 {
		t.Fatal(e)
	}
	reopened, err := New(s.design, store, e, s.items, s.available)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = s.now
	reopened.SetSessionSource(s.session)
	_, replay, _, err := reopened.Handle("/ShopBuy", request)
	if err != nil || !bytes.Equal(response, replay) || e.calls != 1 {
		t.Fatal("retry did not preserve receipt", err)
	}
	for _, bad := range [][]byte{buy(1, 1, 100), buy(2, 2, 100), buy(3, 1, 1)} {
		if _, _, _, err = s.Handle("/ShopBuy", bad); err == nil {
			t.Fatalf("invalid purchase accepted %x", bad)
		}
	}
	batch := buy(4, 1, 100)
	g := wire.AppendVarint(nil, 1, 71)
	p := wire.AppendVarint(wire.AppendVarint(nil, 2, 999), 3, 1)
	batch = wire.AppendBytes(batch, 2, wire.AppendBytes(g, 2, p))
	if _, _, _, err = s.Handle("/ShopBuy", batch); err == nil || e.calls != 1 {
		t.Fatal("partially granted invalid batch")
	}
	s.now = func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) }
	if _, _, _, err = s.Handle("/ShopBuy", buy(5, 3, 100)); err != nil {
		t.Fatal("daily reset did not reopen stock", err)
	}
}
func TestSaleConsumesTheRequestedStackAndRejectsDuplicates(t *testing.T) {
	s, e, _ := fixture(t)
	granted, err := s.items.GrantOnce("food", []gamedata.BattleReward{{Type: 5, ID: 400, Count: 8}})
	if err != nil {
		t.Fatal(err)
	}
	item := granted[0]
	line := wire.AppendVarint(nil, 1, item.InvenIndex)
	line = wire.AppendVarint(line, 2, 19)
	line = wire.AppendVarint(line, 3, 3)
	line = wire.AppendVarint(line, 4, 100)
	req := wire.AppendBytes(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 71), 3, line)
	duplicate := wire.AppendBytes(req, 3, line)
	if _, _, _, err = s.Handle("/ShopSell", duplicate); err == nil || e.calls != 0 {
		t.Fatal("duplicate stack sale accepted")
	}
	code, first, _, err := s.Handle("/ShopSell", req)
	if err != nil || code != 55 {
		t.Fatal(code, err)
	}
	if len(e.costs) != 0 || e.rewards[0].Count != 21 {
		t.Fatal("wrong sale reward", e)
	}
	var remaining uint64
	for _, v := range s.items.All() {
		if v.InvenIndex == item.InvenIndex {
			remaining = v.Count
		}
	}
	if remaining != 5 {
		t.Fatal("wrong concrete stack consumed", remaining)
	}
	_, replay, _, err := s.Handle("/ShopSell", req)
	if err != nil || !bytes.Equal(first, replay) || e.calls != 1 {
		t.Fatal("sale retry spent twice", err)
	}
}
func TestResourcePurchaseValidatesAndConsumesExactMaterialRows(t *testing.T) {
	s, e, _ := fixture(t)
	p := s.design.Products[71][3]
	p.Price = gamedata.Reward{Type: 8, ID: 401, Count: 5}
	s.design.Products[71][3] = p
	granted, err := s.items.GrantOnce("resource", []gamedata.BattleReward{{Type: 8, ID: 401, Count: 18}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.Handle("/ShopBuy", buy(1, 2, 100)); err == nil || e.calls != 0 {
		t.Fatal("missing resource payment accepted")
	}
	material := wire.AppendVarint(nil, 1, granted[0].InvenIndex)
	material = wire.AppendVarint(material, 2, 401)
	material = wire.AppendVarint(material, 3, 8)
	material = wire.AppendVarint(material, 4, 10)
	if _, _, _, err = s.Handle("/ShopBuy", wire.AppendBytes(buy(2, 2, 100), 3, material)); err != nil {
		t.Fatal(err)
	}
	if len(e.costs) != 0 || e.rewards[0].Count != 4 {
		t.Fatal("material cost sent to generic stack selection", e)
	}
	rows := s.items.All()
	if len(rows) != 1 || rows[0].Count != 8 {
		t.Fatal("resource stack was not spent", rows)
	}
}
