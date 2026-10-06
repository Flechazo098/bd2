package npcshop

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
)

func saleLine(index, product, n, rate uint64) []byte {
	b := wire.AppendVarint(nil, 1, index)
	b = wire.AppendVarint(b, 2, product)
	b = wire.AppendVarint(b, 3, n)
	return wire.AppendVarint(b, 4, rate)
}
func saleRequest(seq uint64, rows ...[]byte) []byte {
	b := wire.AppendVarint(wire.AppendVarint(nil, 1, seq), 2, 71)
	for _, row := range rows {
		b = wire.AppendBytes(b, 3, row)
	}
	return b
}

func TestSaleAllClientTypesAndWholeBatchValidation(t *testing.T) {
	s, e, store := fixture(t)
	s.design.Sell[23] = gamedata.NPCProduct{ID: 23, Reward: gamedata.Reward{Type: 8, ID: 401, Count: 1}, Price: gamedata.Reward{Type: 4, Count: 9}}
	items, err := s.items.GrantOnce("sale-types", []gamedata.BattleReward{{Type: 5, ID: 400, Count: 8}, {Type: 8, ID: 401, Count: 10}})
	if err != nil {
		t.Fatal(err)
	}
	food, resource := items[0], items[1]
	good := saleLine(resource.InvenIndex, 23, 4, 100)
	for _, bad := range [][]byte{
		saleLine(resource.InvenIndex, 19, 1, 100),
		saleLine(resource.InvenIndex, 23, 0, 100),
		saleLine(resource.InvenIndex, 23, 11, 100),
		saleLine(resource.InvenIndex, 23, 1<<31, 100),
		saleLine(resource.InvenIndex, 23, 1, 0),
		wire.AppendVarint(good, 3, 1),
		wire.AppendBytes(good, 4, nil),
	} {
		if _, _, _, err = s.Handle("/ShopSell", saleRequest(1, saleLine(food.InvenIndex, 19, 3, 100), bad)); err == nil || e.calls != 0 {
			t.Fatalf("invalid batch mutated economy: %x %v", bad, err)
		}
	}
	// KeepFlag is the actual ItemDBInfo protection flag; these two types have
	// no binding/equipment LockFlag or UseChar fields in the current protocol.
	resource.KeepFlag = 1
	payload, _ := json.Marshal(resource)
	if err = store.SaveWithEntries("items", nil, []stateio.EntryMutation{{Bucket: "items", Key: strconv.FormatUint(resource.InvenIndex, 10), Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	s.items, err = player.OpenInventory(store, &player.Starter{Version: versionconfig.State()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.Handle("/ShopSell", saleRequest(2, good)); err == nil || e.calls != 0 {
		t.Fatal("protected resource sold", err)
	}
	resource.KeepFlag = 0
	payload, _ = json.Marshal(resource)
	if err = store.SaveWithEntries("items", nil, []stateio.EntryMutation{{Bucket: "items", Key: strconv.FormatUint(resource.InvenIndex, 10), Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	s.items, err = player.OpenInventory(store, &player.Starter{Version: versionconfig.State()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.Handle("/ShopSell", saleRequest(3, saleLine(food.InvenIndex, 19, 3, 100), good)); err != nil {
		t.Fatal(err)
	}
	if e.calls != 1 || len(e.rewards) != 2 || e.rewards[0].Count != 21 || e.rewards[1].Count != 36 {
		t.Fatal("wrong mixed sale", e)
	}
	remaining := s.items.All()
	if len(remaining) != 2 || remaining[0].Count != 5 || remaining[1].Count != 6 {
		t.Fatal("wrong mixed inventory debit", remaining)
	}
}

func TestSaleQuoteSeedRemainsUntilShopInfoRefresh(t *testing.T) {
	s, _, _ := fixture(t)
	p := gamedata.NPCProduct{Reward: gamedata.Reward{Type: 8, ID: 401, Count: 1}, Discount: 20, Premium: 19}
	// Independent vectors from the client's ulong WELL512 operations.
	for _, row := range []struct{ seed, rate uint64 }{{0, 117}, {20731, 80}, {20732, 101}} {
		s.quotedSeed = &row.seed
		if got := s.rate(p, 71, 2); got != row.rate {
			t.Fatal("client WELL512 mismatch", row, got)
		}
	}
	s.quotedSeed = nil
	s.now = func() time.Time { return time.Unix(20731*86400, 0) }
	_, info, _, err := s.Handle("/ShopInfo", wire.AppendVarint(nil, 1, 1))
	seed, _, _ := wire.Varint(info, 2)
	if err != nil || seed != 20731 {
		t.Fatal(seed, err)
	}
	s.now = func() time.Time { return time.Unix(20732*86400, 0) }
	s.BeginSession("login") // The router activates the same session on each request.
	if s.rate(p, 71, 2) != 80 {
		t.Fatal("clock change invalidated displayed sale price")
	}
	if _, _, _, err = s.Handle("/ShopInfo", wire.AppendVarint(nil, 1, 2)); err != nil || s.rate(p, 71, 2) != 101 {
		t.Fatal("new quote failed to refresh seed", err)
	}
}

func TestShopPriceUsesUnsignedWELLMaskForFoodResourceAndBuy(t *testing.T) {
	s, _, _ := fixture(t)
	seed := uint64(20732)
	s.quotedSeed = &seed
	for _, row := range []struct {
		typ, item, tab, expected uint64
	}{
		// Current SellItemTable[2], also observed in the client's request:
		// ShopInfo seed=20732; shop=1; Food=5; item=2002; Sell=2; Rate=98.
		{5, 2002, 2, 98},
		{8, 101, 2, 93},
		{5, 2002, 1, 110},
	} {
		p := gamedata.NPCProduct{Reward: gamedata.Reward{Type: row.typ, ID: row.item, Count: 1}, Discount: 20, Premium: 19}
		if got := s.rate(p, 1, row.tab); got != row.expected {
			t.Fatalf("unsigned WELL mask type=%d item=%d tab=%d got=%d want=%d", row.typ, row.item, row.tab, got, row.expected)
		}
	}
}

func TestSaleClientObservedFoodRateCreditsDerivedGold(t *testing.T) {
	s, economy, _ := fixture(t)
	s.design.Shops[1] = gamedata.NPCShop{ID: 1, PackID: 91}
	s.design.Sell[2] = gamedata.NPCProduct{ID: 2, Reward: gamedata.Reward{Type: 5, ID: 2002, Count: 1}, Price: gamedata.Reward{Type: 4, Count: 28}, Discount: 20, Premium: 19, PremiumPriceType: 1, HighDay: 1, HighShop: 14, HighPremium: 20}
	seed := uint64(20732)
	s.quotedSeed = &seed
	items, err := s.items.GrantOnce("observed-food", []gamedata.BattleReward{{Type: 5, ID: 2002, Count: 2}})
	if err != nil {
		t.Fatal(err)
	}
	request := func(rate uint64) []byte {
		b := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 1)
		return wire.AppendBytes(b, 3, saleLine(items[0].InvenIndex, 2, 1, rate))
	}
	if _, _, _, err = s.Handle("/ShopSell", request(93)); err == nil || economy.calls != 0 {
		t.Fatal("incorrect sign-extended rate accepted", err)
	}
	if _, _, _, err = s.Handle("/ShopSell", request(98)); err != nil {
		t.Fatal("actual client rate rejected", err)
	}
	if economy.calls != 1 || len(economy.rewards) != 1 || economy.rewards[0].Type != 4 || economy.rewards[0].Count != 27 || s.items.All()[0].Count != 1 {
		t.Fatal("wrong derived sale credit/debit", economy.rewards, s.items.All())
	}
}

type saleWalletEconomy struct{ wallet *player.Wallet }

func (e saleWalletEconomy) Apply(identity string, costs, rewards []gamedata.Reward) ([]byte, error) {
	if err := e.wallet.ExchangeOnce(identity, costs, rewards); err != nil {
		return nil, err
	}
	return wire.AppendVarint(nil, 1, e.wallet.Snapshot().Gold), nil
}

func TestSQLiteMixedSaleWalletInventoryReceiptRollbackAndRestartRetry(t *testing.T) {
	base, _, _ := fixture(t)
	base.design.Sell[23] = gamedata.NPCProduct{ID: 23, Reward: gamedata.Reward{Type: 8, ID: 401, Count: 1}, Price: gamedata.Reward{Type: 4, Count: 9}}
	path := filepath.Join(t.TempDir(), "state.db")
	starter := &player.Starter{Version: versionconfig.State()}
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	items, err := player.OpenInventory(repo, starter)
	if err != nil {
		t.Fatal(err)
	}
	granted, err := items.GrantOnce("initial", []gamedata.BattleReward{{Type: 5, ID: 400, Count: 8}, {Type: 8, ID: 401, Count: 10}})
	if err != nil {
		t.Fatal(err)
	}
	req := saleRequest(1, saleLine(granted[0].InvenIndex, 19, 3, 100), saleLine(granted[1].InvenIndex, 23, 4, 100))
	var wallet *player.Wallet
	var response []byte
	for phase := range 3 {
		items, err = player.OpenInventory(repo, starter)
		if err != nil {
			t.Fatal(err)
		}
		wallet, err = player.OpenWallet(repo, player.Currency{Gold: 100})
		if err != nil {
			t.Fatal(err)
		}
		var store stateio.Store = repo
		if phase == 0 {
			store = failedReceiptStore{repo}
		}
		s, err := New(base.design, store, saleWalletEconomy{wallet}, items, base.available)
		if err != nil {
			t.Fatal(err)
		}
		s.now, s.session = base.now, base.session
		op, err := repo.BeginOperation()
		if err != nil {
			t.Fatal(err)
		}
		_, body, _, handleErr := s.Handle("/ShopSell", req)
		if phase == 0 {
			if handleErr == nil {
				t.Fatal("injected receipt failure accepted")
			}
			if err = op.Rollback(); err != nil && !errors.Is(err, stateio.ErrStateRecoveryRequired) {
				t.Fatal(err)
			}
		} else {
			if handleErr != nil {
				t.Fatal(handleErr)
			}
			if phase == 1 {
				response = body
			} else if !bytes.Equal(response, body) {
				t.Fatal("restart replay changed response")
			}
			if err = op.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		if err = repo.Close(); err != nil {
			t.Fatal(err)
		}
		repo, err = accountstate.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		checkItems, err := player.OpenInventory(repo, starter)
		if err != nil {
			t.Fatal(err)
		}
		checkWallet, err := player.OpenWallet(repo, player.Currency{Gold: 100})
		if err != nil {
			t.Fatal(err)
		}
		wantGold, wantFood, wantResource := uint64(157), uint64(5), uint64(6)
		if phase == 0 {
			wantGold, wantFood, wantResource = 100, 8, 10
			if raw, err := repo.Load("npc_shop"); err != nil || raw != nil {
				t.Fatal("sale receipt survived rollback", err)
			}
		}
		rows := checkItems.All()
		if checkWallet.Snapshot().Gold != wantGold || len(rows) != 2 || rows[0].Count != wantFood || rows[1].Count != wantResource {
			t.Fatal("SQLite atomic/replay state mismatch", phase, checkWallet.Snapshot(), rows)
		}
	}
}
