package events

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

type economyCostumes struct{}

func (economyCostumes) Character(id uint64) (gamedata.CharacterDesign, bool) {
	return gamedata.CharacterDesign{ID: 6060, HP: 100, CostumeMaxLevel: 5, OverflowItemType: 20, OverflowItemCount: 200}, id == 60601
}

type economyOptions struct{}

func (economyOptions) RollOptions(uint64) ([]gamedata.EquipmentOptionChoice, []gamedata.EquipmentOptionChoice, *gamedata.EquipmentOptionChoice, error) {
	return nil, nil, nil, nil
}

type economyGraph struct{ calls int }

func (g *economyGraph) Resolve(rs []gamedata.BattleReward) ([]gamedata.BattleReward, error) {
	g.calls++
	var out []gamedata.BattleReward
	for _, r := range rs {
		if r.Type == 9 {
			out = append(out, gamedata.BattleReward{Type: 4, Count: r.Count * 10})
		} else {
			out = append(out, r)
		}
	}
	return out, nil
}
func economyFixture(t *testing.T, store stateio.Store, g *economyGraph) (*Economy, *player.Inventory, *player.Wallet) {
	t.Helper()
	items, e := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	if e != nil {
		t.Fatal(e)
	}
	wallet, e := player.OpenWallet(store, player.Currency{Gold: 5})
	if e != nil {
		t.Fatal(e)
	}
	collection, e := player.OpenCollectionStore(store, nil)
	if e != nil {
		t.Fatal(e)
	}
	equips, e := player.OpenEquipmentInventory(store)
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range []func() error{items.EnsurePersisted, wallet.EnsurePersisted, collection.EnsurePersisted, equips.EnsurePersisted} {
		if e = f(); e != nil {
			t.Fatal(e)
		}
	}
	eco, e := NewEconomy(store, items, wallet, collection, equips, economyCostumes{}, economyOptions{}, g, map[uint64]uint64{15: 3})
	if e != nil {
		t.Fatal(e)
	}
	return eco, items, wallet
}
func TestEconomyReplayBeforeCostSelectionAndRandom(t *testing.T) {
	g := &economyGraph{}
	eco, _, wallet := economyFixture(t, stateio.NewMemory(), g)
	cost := []gamedata.Reward{{Type: 4, Count: 5}}
	rewards := []gamedata.Reward{{Type: 8, ID: 1000, Count: 2}}
	a, e := eco.Apply("request", cost, rewards)
	if e != nil {
		t.Fatal(e)
	}
	b, e := eco.Apply("request", cost, rewards)
	if e != nil || !bytes.Equal(a, b) || g.calls != 1 || wallet.Snapshot().Gold != 0 {
		t.Fatalf("retry cost/RNG %v calls %d", e, g.calls)
	}
	if _, e = eco.Apply("request", cost, nil); e == nil {
		t.Fatal("identity definition conflict accepted")
	}
}
func TestEconomyRejectsUnknownCostAndCurrencyID(t *testing.T) {
	eco, _, wallet := economyFixture(t, stateio.NewMemory(), &economyGraph{})
	if _, e := eco.ConsumeAndGrant("unknown", []player.Item{{Type: 999, ID: 1, InvenIndex: 1, Count: 1}}, nil); e == nil {
		t.Fatal("unknown cost accepted")
	}
	if _, e := eco.Apply("badcurrency", nil, []gamedata.Reward{{Type: 4, ID: 3, Count: 1}}); e == nil {
		t.Fatal("currency ID accepted")
	}
	if wallet.Snapshot().Gold != 5 {
		t.Fatal("validation mutated wallet")
	}
}
func TestEconomySQLiteAtomicRollbackAndRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, e := accountstate.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	g := &economyGraph{}
	eco, _, wallet := economyFixture(t, repo, g)
	op, e := repo.BeginOperation()
	if e != nil {
		t.Fatal(e)
	}
	cost := []gamedata.Reward{{Type: 4, Count: 5}}
	rewards := []gamedata.Reward{{Type: 8, ID: 1000, Count: 2}, {Type: 15, Count: 2}}
	if _, e = eco.Apply("atomic", cost, rewards); e != nil {
		t.Fatal(e)
	}
	if wallet.Snapshot().Gold != 0 {
		t.Fatal("operation not applied")
	}
	_ = op.Rollback()
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, e = accountstate.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	eco, items, wallet := economyFixture(t, repo, &economyGraph{})
	if wallet.Snapshot().Gold != 5 || len(items.All()) != 0 {
		t.Fatal("rollback kept rewards/cost")
	}
	currencies, e := eco.AdditionalCurrencies()
	if e != nil || currencies[16] != 3 {
		t.Fatalf("extra currency survived rollback %v %v", currencies, e)
	}
	op, e = repo.BeginOperation()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = eco.Apply("atomic", cost, rewards); e != nil {
		t.Fatal(e)
	}
	if e = op.Commit(); e != nil {
		t.Fatal(e)
	}
	if wallet.Snapshot().Gold != 0 || len(items.All()) != 1 {
		t.Fatal("retry failed")
	}
}
func TestEconomyOptionalCurrencyOverflow(t *testing.T) {
	eco, _, _ := economyFixture(t, stateio.NewMemory(), &economyGraph{})
	if _, e := eco.Apply("overflow", nil, []gamedata.Reward{{Type: 15, Count: 2147483647}}); e == nil {
		t.Fatal("optional currency overflow accepted")
	}
	currencies, e := eco.AdditionalCurrencies()
	if e != nil || currencies[16] != 3 {
		t.Fatal(fmt.Sprint(currencies, e))
	}
}

func TestEconomyDailyFreeAPOnly(t *testing.T) {
	eco, _, _ := economyFixture(t, stateio.NewMemory(), &economyGraph{})
	if e := eco.AttachAPRefresh(map[uint64]uint64{30: 5, 32: 60}, gamedata.HuntingAPDesign{ResetSeconds: 32400}); e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 10, 4, 23, 59, 0, 0, time.UTC)
	eco.now = func() time.Time { return now }
	if e := eco.CanApply([]gamedata.Reward{{Type: 30, Count: 5}}); e != nil {
		t.Fatal(e)
	}
	if _, e := eco.Apply("day1", []gamedata.Reward{{Type: 30, Count: 5}}, nil); e != nil {
		t.Fatal(e)
	}
	if e := eco.CanApply([]gamedata.Reward{{Type: 30, Count: 1}}); e == nil {
		t.Fatal("CanApply ignored spent AP")
	}
	now = now.Add(2 * time.Minute)
	if e := eco.CanApply([]gamedata.Reward{{Type: 30, Count: 5}}); e != nil {
		t.Fatal(e)
	}
	currencies, e := eco.AdditionalCurrencies()
	if e != nil || currencies[16] != 3 || currencies[34] != 5 || currencies[36] != 60 {
		t.Fatalf("refresh changed wrong currency %+v %v", currencies, e)
	}
	if _, e = eco.ChargeInfo(); e != nil {
		t.Fatal(e)
	}
}

func (g *economyGraph) ResolveGranted(rs []gamedata.BattleReward) ([]gamedata.BattleReward, error) {
	g.calls++
	return rs, nil
}
func TestEconomyBoxPreservedUntilExplicitUse(t *testing.T) {
	store := stateio.NewMemory()
	g := &economyGraph{}
	eco, items, wallet := economyFixture(t, store, g)
	if _, e := eco.Apply("grantbox", nil, []gamedata.Reward{{Type: 9, ID: 100, Count: 2}}); e != nil {
		t.Fatal(e)
	}
	owned := items.All()
	if len(owned) != 1 || owned[0].Type != 9 || wallet.Snapshot().Gold != 5 {
		t.Fatal("reward auto-opened manual box")
	}
	boxes, e := OpenBoxes(store, items, eco)
	if e != nil {
		t.Fatal(e)
	}
	req := wire.AppendVarint(nil, 1, 1)
	req = wire.AppendVarint(req, 2, owned[0].InvenIndex)
	req = wire.AppendVarint(req, 3, 2)
	code, a, _, e := boxes.HandleSession("/UseRandomBox", req, "session")
	if e != nil || code != 143 || wallet.Snapshot().Gold != 25 || len(items.All()) != 0 {
		t.Fatalf("openbox %d %v gold %d", code, e, wallet.Snapshot().Gold)
	}
	_, b, _, e := boxes.HandleSession("/UseRandomBox", req, "session")
	if e != nil || !bytes.Equal(a, b) || g.calls != 2 {
		t.Fatalf("box retry rerolled %v calls%d", e, g.calls)
	}
}

func TestPrestigeSkinRewardAndSpecialQuery(t *testing.T) {
	eco, _, _ := economyFixture(t, stateio.NewMemory(), &economyGraph{})
	eco.AttachPrestigeSkins(map[uint64]uint64{9901: 60601})
	if _, e := eco.Apply("skin", nil, []gamedata.Reward{{Type: 45, ID: 9901, Count: 1}}); e != nil {
		t.Fatal(e)
	}
	code, out, ok, e := eco.PrestigeSkinInfo("/PrestigeSkinInfo", wire.AppendVarint(nil, 1, 1))
	entry, found, _ := wire.Bytes(out, 1)
	costume, _, _ := wire.Varint(entry, 1)
	design, _, _ := wire.Varint(entry, 2)
	if e != nil || !ok || code != 425 || !found || costume != 60601 || design != 9901 {
		t.Fatalf("skin wire %x %v", out, e)
	}
}

func TestCurrencyEnumProjectionMiniGameAndDeco(t *testing.T) {
	eco, _, _ := economyFixture(t, stateio.NewMemory(), &economyGraph{})
	if _, e := eco.Apply("coins", nil, []gamedata.Reward{{Type: 43, Count: 7}, {Type: 44, Count: 11}}); e != nil {
		t.Fatal(e)
	}
	fields, e := eco.AdditionalCurrencies()
	if e != nil || fields[48] != 7 || fields[54] != 11 {
		t.Fatalf("enum projection %+v %v", fields, e)
	}
}

func TestOwnedEventItemsAndColosseumCoin(t *testing.T) {
	eco, items, _ := economyFixture(t, stateio.NewMemory(), &economyGraph{})
	eco.AttachOwnedItemDesign(map[uint64]map[uint64]bool{47: {100: true}, 49: {200: true}})
	if _, e := eco.Apply("eventowned", nil, []gamedata.Reward{{Type: 47, ID: 100, Count: 1}, {Type: 49, ID: 200, Count: 1}, {Type: 60, Count: 10}}); e != nil {
		t.Fatal(e)
	}
	if len(items.All()) != 2 {
		t.Fatal("owned event rewards absent")
	}
	currencies, e := eco.AdditionalCurrencies()
	if e != nil || currencies[60] != 10 {
		t.Fatal("colosseum coin missing")
	}
	code, out, _, e := eco.OwnedItemInfo("/AvatarInfo", wire.AppendVarint(nil, 1, 1))
	item, found, _ := wire.Bytes(out, 2)
	typ, _, _ := wire.Varint(item, 3)
	id, _, _ := wire.Varint(item, 2)
	if e != nil || code != 467 || !found || typ != 49 || id != 200 {
		t.Fatalf("avatar query%x %v", out, e)
	}
	if _, e = eco.Apply("badavatar", nil, []gamedata.Reward{{Type: 49, ID: 201, Count: 1}}); e == nil {
		t.Fatal("unknown avatar accepted")
	}
}
