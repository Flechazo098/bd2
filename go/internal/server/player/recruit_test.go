package player

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type failingRecruitCatalog struct {
	design CostumeDesignSource
	fail   bool
}

func (c *failingRecruitCatalog) Character(id uint64) (gamedata.CharacterDesign, bool) {
	if c.fail {
		return gamedata.CharacterDesign{}, false
	}
	return c.design.Character(id)
}
func TestRecruitSQLiteFailureAfterConsumeRollsBackAllDomains(t *testing.T) {
	fixture, _ := recruitFixture(t, filepath.Join(t.TempDir(), "design.json"))
	path := filepath.Join(t.TempDir(), "state.db")
	repo, e := accountstate.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	starter := &Starter{Version: "2.35.10"}
	inventory, e := OpenInventory(repo, starter)
	if e != nil {
		t.Fatal(e)
	}
	items, e := inventory.GrantOnce("tickets", []gamedata.BattleReward{{Type: 8, ID: 71, Count: 2}})
	if e != nil {
		t.Fatal(e)
	}
	wallet, e := OpenWallet(repo, Currency{FreeJewelry: 500})
	if e != nil {
		t.Fatal(e)
	}
	collection, e := OpenCollectionStore(repo, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, ensure := range []func() error{inventory.EnsurePersisted, wallet.EnsurePersisted, collection.EnsurePersisted} {
		if e = ensure(); e != nil {
			t.Fatal(e)
		}
	}
	catalog := &failingRecruitCatalog{design: fixture.catalog}
	s, e := NewRecruitService(fixture.design, catalog, collection, inventory, wallet, fixture.resolver)
	if e != nil {
		t.Fatal(e)
	}
	s.BeginSession("session")
	s.now = fixture.now
	op, e := repo.BeginOperation()
	if e != nil {
		t.Fatal(e)
	}
	catalog.fail = true
	ticket := items[0]
	ticket.Count = 1
	if _, _, _, e = s.Handle("/MercenaryScout", costumeBurstRequest(1, 99, ticket)); e == nil {
		t.Fatal("expected grant failure after material deduction")
	}
	if e = op.Rollback(); e == nil {
		t.Fatal("dirty rollback must fence published in-memory state")
	}
	if e = repo.Close(); e != nil {
		t.Fatal(e)
	}
	repo, e = accountstate.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer repo.Close()
	inventory, e = OpenInventory(repo, starter)
	if e != nil {
		t.Fatal(e)
	}
	wallet, e = OpenWallet(repo, Currency{})
	if e != nil {
		t.Fatal(e)
	}
	collection, e = OpenCollectionStore(repo, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = inventory.CanConsume(items); e != nil {
		t.Fatalf("rollback lost ticket: %v", e)
	}
	if wallet.Snapshot().FreeJewelry != 500 || len(collection.Costumes()) != 0 || len(collection.Characters()) != 0 {
		t.Fatal("rollback committed partial rewards/currency")
	}
	if _, ok := collection.Grant(recruitIdentity(10)); ok {
		t.Fatal("rollback committed completion")
	}
	if _, ok := collection.Grant("special-recruit-state"); ok {
		t.Fatal("normal rollback wrote rotation")
	}
	catalog.fail = false
	s, e = NewRecruitService(fixture.design, catalog, collection, inventory, wallet, fixture.resolver)
	if e != nil {
		t.Fatal(e)
	}
	s.BeginSession("session")
	s.now = fixture.now
	op, e = repo.BeginOperation()
	if e != nil {
		t.Fatal(e)
	}
	if _, _, _, e = s.Handle("/MercenaryScout", costumeBurstRequest(1, 99, ticket)); e != nil {
		t.Fatal(e)
	}
	if e = op.Commit(); e != nil {
		t.Fatal(e)
	}
	if _, ok := collection.Grant(recruitIdentity(10)); !ok {
		t.Fatal("successful retry not committed")
	}
}

func recruitFixture(t *testing.T, path string) (*RecruitService, Item) {
	t.Helper()
	store := testStore(path)
	inventory, e := OpenInventory(store, &Starter{Version: "2.35.10"})
	if e != nil {
		t.Fatal(e)
	}
	items, e := inventory.GrantOnce("tickets", []gamedata.BattleReward{{Type: 8, ID: 71, Count: 10}})
	if e != nil {
		t.Fatal(e)
	}
	wallet, e := OpenWallet(store, Currency{FreeJewelry: 500})
	if e != nil {
		t.Fatal(e)
	}
	collection, e := OpenCollectionStore(store, nil)
	if e != nil {
		t.Fatal(e)
	}
	d := &gamedata.RecruitDesign{Rules: map[uint64]gamedata.RecruitRule{}, Characters: map[uint64]gamedata.CharacterDesign{}, AppearCount: 2, AutoResetMinute: 120, ResetCount: 50, ResetType: 3, ResetLimit: 2}
	for _, id := range []uint64{10, 20, 30, 40} {
		r := gamedata.RecruitRule{ID: id, CostumeID: id*10 + 1, Type: 1, AppearProb: id, Costs: []gamedata.PromotionCost{{Type: 8, ID: 71, Count: 1}}}
		if id == 10 {
			r.Type = 0
		}
		d.Rules[id] = r
		d.Characters[r.CostumeID] = gamedata.CharacterDesign{ID: id, HP: 100, CostumeMaxLevel: 5, OverflowItemType: 20, OverflowItemCount: 100}
	}
	s, e := NewRecruitService(d, d, collection, inventory, wallet, func(n uint64) (uint64, error) {
		if n != 99 {
			return 0, errors.New("unauthorized NPC")
		}
		return 10, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	s.BeginSession("session")
	s.now = func() time.Time { return time.UnixMilli(2000000000000) }
	item := items[0]
	item.Count = 1
	return s, item
}
func TestRecruitSpecialPersistsAppearanceCompletesAndReplays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "account.json")
	s, ticket := recruitFixture(t, path)
	req := wire.AppendVarint(nil, 1, 1)
	code, body, _, e := s.Handle("/CharScoutInfo", req)
	if e != nil || code != 148 {
		t.Fatalf("info %d %v", code, e)
	}
	ids, e := recruitIDs(body, 1)
	if e != nil || len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("appearance %v %v", ids, e)
	}
	request := costumeBurstRequest(2, ids[0], ticket)
	code, first, _, e := s.Handle("/CharSpecialScoutBuy", request)
	if e != nil || code != 149 {
		t.Fatalf("buy %d %v", code, e)
	}
	if _, ok := s.collection.Grant(recruitIdentity(ids[0])); !ok {
		t.Fatal("completion missing")
	}
	if _, ok := s.collection.CostumeByID(ids[0]*10 + 1); !ok {
		t.Fatal("costume missing")
	}
	_, second, _, e := s.Handle("/CharSpecialScoutBuy", request)
	if e != nil || !bytes.Equal(first, second) {
		t.Fatalf("replay %v", e)
	}
	if _, _, _, e = s.Handle("/CharSpecialScoutBuy", costumeBurstRequest(3, ids[0], ticket)); e == nil {
		t.Fatal("repeat recruitment accepted")
	}
	store := testStore(path)
	inventory, e := OpenInventory(store, &Starter{Version: "2.35.10"})
	if e != nil {
		t.Fatal(e)
	}
	wallet, e := OpenWallet(store, Currency{})
	if e != nil {
		t.Fatal(e)
	}
	collection, e := OpenCollectionStore(store, nil)
	if e != nil {
		t.Fatal(e)
	}
	restored, e := NewRecruitService(s.design, s.catalog, collection, inventory, wallet, s.resolver)
	if e != nil {
		t.Fatal(e)
	}
	restored.BeginSession("session")
	restored.now = s.now
	_, again, _, e := restored.Handle("/CharSpecialScoutBuy", request)
	if e != nil || !bytes.Equal(again, first) {
		t.Fatalf("durable replay %v", e)
	}
	_, info, _, e := restored.Handle("/CharScoutInfo", req)
	completed, _ := recruitIDs(info, 4)
	appearing, _ := recruitIDs(info, 1)
	if e != nil || len(completed) != 1 || completed[0] != ids[0] || len(appearing) != 1 {
		t.Fatalf("restored %x %v", info, e)
	}
	remaining := ticket
	remaining.Count = 9
	if e = inventory.CanConsume([]Item{remaining}); e != nil {
		t.Fatalf("double charged %v", e)
	}
}
func TestRecruitNormalAuthorizesNPCAndRejectsForgedCosts(t *testing.T) {
	s, ticket := recruitFixture(t, filepath.Join(t.TempDir(), "account.json"))
	for _, req := range [][]byte{costumeBurstRequest(1, 98, ticket), costumeBurstRequest(2, 99, Item{InvenIndex: ticket.InvenIndex, ID: 71, Type: 8, Count: 2}), costumeBurstRequest(3, 20, ticket)} {
		if _, _, _, e := s.Handle("/MercenaryScout", req); e == nil {
			t.Fatal("invalid normal recruit accepted")
		}
	}
	if _, exists := s.collection.Grant("special-recruit-state"); exists {
		t.Fatal("rejected normal recruit wrote rotation")
	}
	code, body, _, e := s.Handle("/MercenaryScout", costumeBurstRequest(4, 99, ticket))
	if e != nil || code != 13 {
		t.Fatalf("normal %d %v", code, e)
	}
	bundle, ok, e := wire.Bytes(body, 1)
	if e != nil || !ok {
		t.Fatal("missing reward bundle")
	}
	if _, ok, e = wire.Bytes(bundle, 2); e != nil || !ok {
		t.Fatal("missing character")
	}
	if _, ok, e = wire.Bytes(bundle, 3); e != nil || !ok {
		t.Fatal("missing costume")
	}
}

func TestRecruitInvalidSpecialRequestsDoNotPersistRotation(t *testing.T) {
	s, ticket := recruitFixture(t, filepath.Join(t.TempDir(), "account.json"))
	if _, _, _, e := s.Handle("/CharSpecialScoutBuy", costumeBurstRequest(1, 999, ticket)); e == nil {
		t.Fatal("unknown recruit accepted")
	}
	if _, exists := s.collection.Grant("special-recruit-state"); exists {
		t.Fatal("invalid buy wrote rotation")
	}
	s.wallet.state.Currency.FreeJewelry = 0
	if _, _, _, e := s.Handle("/CharSpecialScoutReset", wire.AppendVarint(nil, 1, 2)); e == nil {
		t.Fatal("unaffordable reset accepted")
	}
	if _, exists := s.collection.Grant("special-recruit-state"); exists {
		t.Fatal("invalid reset wrote rotation")
	}
}
func TestRecruitResetUsesFreeJewelryLimitReplayAndAutomaticDeadline(t *testing.T) {
	s, _ := recruitFixture(t, filepath.Join(t.TempDir(), "account.json"))
	request := wire.AppendVarint(nil, 1, 1)
	code, first, _, e := s.Handle("/CharSpecialScoutReset", request)
	if e != nil || code != 150 {
		t.Fatalf("reset %d %v", code, e)
	}
	if s.wallet.Snapshot().FreeJewelry != 450 {
		t.Fatal("reset cost")
	}
	_, again, _, e := s.Handle("/CharSpecialScoutReset", request)
	if e != nil || !bytes.Equal(first, again) || s.wallet.Snapshot().FreeJewelry != 450 {
		t.Fatal("reset replay charged")
	}
	if _, _, _, e = s.Handle("/CharSpecialScoutReset", wire.AppendVarint(nil, 1, 2)); e != nil {
		t.Fatal(e)
	}
	if _, _, _, e = s.Handle("/CharSpecialScoutReset", wire.AppendVarint(nil, 1, 3)); e == nil {
		t.Fatal("reset limit ignored")
	}
	s.now = func() time.Time { return time.UnixMilli(2000000000000).Add(121 * time.Minute) }
	_, body, _, e := s.Handle("/CharScoutInfo", wire.AppendVarint(nil, 1, 4))
	count, _, _ := wire.Varint(body, 2)
	next, _, _ := wire.Varint(body, 3)
	if e != nil || count != 2 || next <= uint64(s.now().UnixMilli()) {
		t.Fatalf("automatic reset %x %v", body, e)
	}
	s.now = func() time.Time { return time.UnixMilli(2000000000000).Add(24 * time.Hour) }
	_, body, _, e = s.Handle("/CharScoutInfo", wire.AppendVarint(nil, 1, 5))
	count, _, _ = wire.Varint(body, 2)
	if e != nil || count != 0 {
		t.Fatal("daily reset did not clear manual count")
	}
	if s.wallet.Snapshot().FreeJewelry != 400 {
		t.Fatal("automatic reset charged")
	}
}
