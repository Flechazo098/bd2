package commerce

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/mail"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

type deliveryGraph struct{ selections int }

func (g *deliveryGraph) ResolveGranted(r []gamedata.BattleReward) ([]gamedata.BattleReward, error) {
	return r, nil
}
func (g *deliveryGraph) ResolveDelivery(r []gamedata.BattleReward) (gamedata.CashDelivery, error) {
	g.selections++
	return gamedata.CashDelivery{Mail: []gamedata.CashMailReward{{TemplateID: 40, Rewards: []gamedata.BattleReward{{Type: 4, Count: 40}, {Type: 9, ID: 200, Count: 1}}}}}, nil
}

type deliveryBase struct {
	wallet *player.Wallet
	items  *player.Inventory
}

func (e deliveryBase) Apply(identity string, costs, rewards []gamedata.Reward) ([]byte, error) {
	var currencies []gamedata.Reward
	for _, r := range rewards {
		if r.Type != 9 {
			currencies = append(currencies, r)
		}
	}
	if err := e.wallet.ExchangeOnce(identity, costs, currencies); err != nil {
		return nil, err
	}
	var items []gamedata.BattleReward
	var bundle []byte
	for _, r := range rewards {
		if r.Type == 9 {
			items = append(items, gamedata.BattleReward(r))
		} else {
			bundle = wire.AppendBytes(bundle, 1, player.ItemWire(player.Item{Type: r.Type, ID: r.ID, Count: r.Count}))
		}
	}
	granted, err := e.items.GrantOnce(identity, items)
	if err != nil {
		return nil, err
	}
	if len(granted) == 0 {
		granted = e.items.GrantedItems(identity)
	}
	for _, item := range granted {
		bundle = wire.AppendBytes(bundle, 1, player.ItemWire(item))
	}
	return bundle, nil
}
func cashList(t *testing.T, m *mail.Service, start, count uint64) ([]uint64, []byte) {
	t.Helper()
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, start)
	request = wire.AppendVarint(request, 3, count)
	code, response, handled, err := m.Handle("/CashMailInfo", request)
	if err != nil || !handled || code != 140 {
		t.Fatalf("cash list: %d %t %v", code, handled, err)
	}
	var ids []uint64
	_ = wire.Walk(response, func(f wire.Field) error {
		if f.Number == 1 && f.Type == 2 {
			id, _, _ := wire.Varint(f.Value, 1)
			template, _, _ := wire.Varint(f.Value, 3)
			typ, _, _ := wire.Varint(f.Value, 2)
			cash, _, _ := wire.Varint(f.Value, 15)
			if id == 0 || template != 40 || typ != 0 || cash != 1 {
				t.Fatalf("invalid cash mail %x", f.Value)
			}
			ids = append(ids, id)
		}
		return nil
	})
	return ids, response
}
func TestCashPurchaseMailSQLiteRollbackRestartBatchAndReplay(t *testing.T) {
	fixture, _, _ := serviceFixture(t, 0)
	path := filepath.Join(t.TempDir(), "state.db")
	graph := &deliveryGraph{}
	open := func() (*accountstate.Repository, *Service, *mail.Service, *player.Wallet, *player.Inventory) {
		t.Helper()
		repo, err := accountstate.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repo.Close() })
		wallet, err := player.OpenWallet(repo, player.Currency{Jewelry: 4000})
		if err != nil {
			t.Fatal(err)
		}
		items, err := player.OpenInventory(repo, &player.Starter{Version: "2.35.10"})
		if err != nil {
			t.Fatal(err)
		}
		if err = wallet.EnsurePersisted(); err != nil {
			t.Fatal(err)
		}
		if err = items.EnsurePersisted(); err != nil {
			t.Fatal(err)
		}
		economy, err := NewEntitlementEconomy(repo, deliveryBase{wallet, items}, graph, items, &gamedata.CashEntitlementDesign{})
		if err != nil {
			t.Fatal(err)
		}
		mailbox, err := mail.OpenService(repo, &mail.Starter{Version: "2.35.10", MailCount: 1}, items, wallet)
		if err != nil {
			t.Fatal(err)
		}
		if err = mailbox.AttachCashRewards(economy, map[uint64]bool{40: true}); err != nil {
			t.Fatal(err)
		}
		if err = economy.AttachCashMail(mailbox); err != nil {
			t.Fatal(err)
		}
		shop, err := NewService(fixture.catalog, repo, economy)
		if err != nil {
			t.Fatal(err)
		}
		return repo, shop, mailbox, wallet, items
	}
	rollback := func(op stateio.RequestOperation) {
		t.Helper()
		if err := op.Rollback(); err != nil && !errors.Is(err, stateio.ErrStateRecoveryRequired) {
			t.Fatal(err)
		}
	}
	repo, shop, mailbox, wallet, items := open()
	shop.AttachPurchaseHook(func(string, gamedata.CashProductDesign, uint64) error {
		return fmt.Errorf("injected failure after issuing mail")
	})
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = shop.HandleSession("/CashShopBuy", buyRequest(1, 2, 1, 0, ""), "s"); err == nil {
		t.Fatal("failure ignored")
	}
	rollback(op)
	_ = repo.Close()
	repo, shop, mailbox, wallet, items = open()
	if wallet.Snapshot().Jewelry != 4000 || wallet.Snapshot().Gold != 0 || len(items.All()) != 0 {
		t.Fatal("rollback retained debit/grant")
	}
	if ids, _ := cashList(t, mailbox, 0, 20); len(ids) != 0 {
		t.Fatal("rollback retained cash mail")
	}
	for seq := uint64(1); seq <= 2; seq++ {
		op, err = repo.BeginOperation()
		if err != nil {
			t.Fatal(err)
		}
		if err = mailbox.BeforeDispatch("/CashShopBuy", nil); err != nil {
			t.Fatal(err)
		}
		_, response, _, err := shop.HandleSession("/CashShopBuy", buyRequest(seq, 2, 1, 0, ""), "s")
		if err != nil {
			t.Fatal(err)
		}
		bundle, present, _ := wire.Bytes(response, 1)
		if !present || len(bundle) != 0 {
			t.Fatalf("mail-only purchase must not return direct rewards: %x", response)
		}
		notify, err := mailbox.AfterDispatch("/CashShopBuy", nil, nil)
		flag, _, _ := wire.Varint(notify, 1)
		if err != nil || flag != 1 {
			t.Fatal("new mail notification missing")
		}
		if err = op.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if wallet.Snapshot().Jewelry != 2000 || wallet.Snapshot().Gold != 0 || len(items.All()) != 0 {
		t.Fatal("unclaimed attachments granted early")
	}
	ids, list := cashList(t, mailbox, 0, 1)
	total, _, _ := wire.Varint(list, 2)
	if len(ids) != 1 || ids[0] != 2 || total != 2 {
		t.Fatal("cash first page", ids, total)
	}
	next, _ := cashList(t, mailbox, ids[0], 1)
	if len(next) != 1 || next[0] != 1 {
		t.Fatal("cash cursor skipped/duplicated", next)
	}
	_, ordinary, _, err := mailbox.Handle("/MailInfo", wire.AppendVarint(nil, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, present, _ := wire.Bytes(ordinary, 1); present {
		t.Fatal("cash mail leaked into ordinary list")
	}
	beforeSelections := graph.selections
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = shop.HandleSession("/CashShopBuy", buyRequest(1, 2, 1, 0, ""), "s"); err != nil {
		t.Fatal(err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	if graph.selections != beforeSelections || wallet.Snapshot().Jewelry != 2000 {
		t.Fatal("purchase replay selected or charged again")
	}
	_ = repo.Close()
	repo, shop, mailbox, wallet, items = open()
	claim := wire.AppendVarint(nil, 1, 3)
	claim = wire.AppendVarint(claim, 2, 1)
	claim = wire.AppendVarint(claim, 2, 2)
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = mailbox.Handle("/MailOpen", claim); err != nil {
		t.Fatal(err)
	}
	rollback(op)
	_ = repo.Close()
	repo, shop, mailbox, wallet, items = open()
	if wallet.Snapshot().Gold != 0 || len(items.All()) != 0 {
		t.Fatal("claim rollback retained rewards")
	}
	if ids, _ := cashList(t, mailbox, 0, 20); len(ids) != 2 {
		t.Fatal("claim rollback hid mails")
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	_, claimed, _, err := mailbox.Handle("/MailOpen", claim)
	if err != nil {
		t.Fatal(err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	countBoxes := func(items *player.Inventory) uint64 {
		var n uint64
		for _, r := range items.All() {
			if r.Type == 9 && r.ID == 200 {
				n += r.Count
			}
		}
		return n
	}
	if wallet.Snapshot().Gold != 80 || countBoxes(items) != 2 {
		t.Fatal("batch claim failed", wallet.Snapshot(), items.All())
	}
	_ = repo.Close()
	repo, _, mailbox, wallet, items = open()
	defer repo.Close()
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	_, replayed, _, err := mailbox.Handle("/MailOpen", claim)
	if err != nil {
		t.Fatal(err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(claimed, replayed) || wallet.Snapshot().Gold != 80 || countBoxes(items) != 2 {
		t.Fatal("restart replay duplicated claim")
	}
	if ids, _ := cashList(t, mailbox, 0, 20); len(ids) != 0 {
		t.Fatal("claimed mails still visible")
	}
	historyReq := wire.AppendVarint(nil, 1, 4)
	historyReq = wire.AppendVarint(historyReq, 3, 20)
	_, history, _, err := mailbox.Handle("/MailHistoryInfo", historyReq)
	n, _, _ := wire.Varint(history, 2)
	if err != nil || n != 2 {
		t.Fatal("claim history missing", n, err)
	}
}
func TestDirectSubscriptionDoesNotDuplicateFirstDayAlreadyInMail(t *testing.T) {
	e, _, base, _, _ := entitlementFixture(t)
	e.mu.Lock()
	_, err := e.applyPrepared("direct", nil, []gamedata.Reward{{Type: 19, ID: 38, Count: 1}}, true, []gamedata.BattleReward{{Type: 3, Count: 1}})
	e.mu.Unlock()
	if err != nil || len(base.Rewards) != 0 {
		t.Fatal("mailed first day was granted directly", base.Rewards, err)
	}
	if _, err = e.ApplyResolved("mail", nil, []gamedata.Reward{{Type: 3, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	if len(base.Rewards) != 1 || base.Rewards[0].Count != 1 {
		t.Fatal("claim did not grant exactly one first day", base.Rewards)
	}
	if _, err = e.ClaimSubscriptions("same day"); err != nil || len(base.Rewards) != 1 {
		t.Fatal("same-day login duplicated first day", err)
	}
}
