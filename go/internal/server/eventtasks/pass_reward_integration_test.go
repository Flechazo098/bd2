package eventtasks

import (
	"bytes"
	"testing"

	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

type passRewardDesign struct{}

func (passRewardDesign) Character(uint64) (gamedata.CharacterDesign, bool) {
	return gamedata.CharacterDesign{}, false
}
func (passRewardDesign) RollOptions(uint64) ([]gamedata.EquipmentOptionChoice, []gamedata.EquipmentOptionChoice, *gamedata.EquipmentOptionChoice, error) {
	return nil, nil, nil, nil
}
func (passRewardDesign) Resolve(r []gamedata.BattleReward) ([]gamedata.BattleReward, error) {
	return r, nil
}

func realPassEconomy(t *testing.T, store stateio.Store) (*events.Economy, *player.Inventory, *player.Wallet) {
	t.Helper()
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
	for _, persist := range []func() error{items.EnsurePersisted, wallet.EnsurePersisted, collection.EnsurePersisted, equipment.EnsurePersisted} {
		if err := persist(); err != nil {
			t.Fatal(err)
		}
	}
	design := passRewardDesign{}
	economy, err := events.NewEconomy(store, items, wallet, collection, equipment, design, design, design, nil)
	if err != nil {
		t.Fatal(err)
	}
	return economy, items, wallet
}

// A spy alone cannot detect a claim recorded without a durable inventory grant.
// Exercise the native BASIC -> PREMIUM one-click flow through the real economy.
func TestPassRewardAllPersistsEveryCurrencyAndItemAcrossRestart(t *testing.T) {
	s, _, store := setup(t)
	economy, items, wallet := realPassEconomy(t, store)
	s.economy = economy
	s.design.PassLevels[8] = []gamedata.EventPassLevel{
		{ID: 1, NeedExp: 20, Basic: gamedata.Reward{Type: 3, Count: 5}, Premium: gamedata.Reward{Type: 8, ID: 1000, Count: 2}},
		{ID: 2, NeedExp: 30, Basic: gamedata.Reward{Type: 3, Count: 7}, Premium: gamedata.Reward{Type: 8, ID: 1000, Count: 3}},
		{ID: 3, NeedExp: 40, Basic: gamedata.Reward{Type: 8, ID: 1000, Count: 11}, Premium: gamedata.Reward{Type: 4, Count: 100}},
		{ID: 4, Basic: gamedata.Reward{Type: 3, Count: 999}, Premium: gamedata.Reward{Type: 4, Count: 999}},
	}
	p := s.pass(s.registry.List()[2])
	p.Exp = 50
	p.Premium = true
	basic := passClaimRequest(1, 0, true, 0)
	premium := passClaimRequest(2, 1, true, 0)
	basicReply := claimPass(t, s, basic)
	premiumReply := claimPass(t, s, premium)
	assertBalances := func(items *player.Inventory, wallet *player.Wallet) {
		t.Helper()
		c := wallet.Snapshot()
		if c.FreeJewelry != 12 || c.Gold != 100 {
			t.Fatalf("durable currencies: %+v", c)
		}
		var total uint64
		for _, item := range items.All() {
			if item.Type == 8 && item.ID == 1000 {
				total += item.Count
			}
		}
		if total != 16 {
			t.Fatalf("durable ticket quantity=%d want=16", total)
		}
	}
	assertBalances(items, wallet)
	for _, response := range [][]byte{basicReply, premiumReply} {
		bundle, _, err := wire.Bytes(response, 1)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		if err := wire.Walk(bundle, func(f wire.Field) error {
			if f.Number == 1 {
				count++
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if count != 3 {
			t.Fatalf("bundle omitted rewards: entries=%d want=3", count)
		}
	}
	economy, items, wallet = realPassEconomy(t, store)
	reopened, err := Open(store, s.design, s.registry, economy)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = s.now
	reopened.SetSession("test")
	if !bytes.Equal(claimPass(t, reopened, basic), basicReply) || !bytes.Equal(claimPass(t, reopened, premium), premiumReply) {
		t.Fatal("replay response changed")
	}
	assertBalances(items, wallet)
	for typ := uint64(0); typ <= 1; typ++ {
		claimPass(t, reopened, passClaimRequest(3+typ, typ, true, 0))
	}
	assertBalances(items, wallet)
	if reopened.pass(reopened.registry.List()[2]).Claimed[key(4, 0)] || reopened.pass(reopened.registry.List()[2]).Claimed[key(4, 1)] {
		t.Fatal("locked level was claimed")
	}
}
