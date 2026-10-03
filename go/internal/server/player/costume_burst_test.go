package player

import (
	"math"
	"path/filepath"
	"testing"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
)

func testCostumeBurstDesign() *gamedata.CostumeBurstDesign {
	return &gamedata.CostumeBurstDesign{Levels: map[uint64]map[uint64]gamedata.CostumeBurstLevel{
		4202: {
			1: {CostumeID: 4202, Level: 1, Costs: []gamedata.PromotionCost{{Type: 4, Count: 100}, {Type: 8, ID: 710, Count: 2}}},
			2: {CostumeID: 4202, Level: 2, Costs: []gamedata.PromotionCost{{Type: 4, Count: 200}, {Type: 8, ID: 710, Count: 2}}},
		},
	}}
}

func costumeBurstRequest(seq, costumeID uint64, materials ...Item) []byte {
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, seq), 2, costumeID)
	for _, material := range materials {
		request = wire.AppendBytes(request, 3, ItemWire(material))
	}
	return request
}

func newCostumeBurstFixture(t *testing.T, storePath string, stacks []gamedata.BattleReward) (*CostumeBurstService, *CollectionStore, *Inventory, *Wallet, []Item) {
	t.Helper()
	store := testStore(storePath)
	starter := &Starter{Version: "2.35.10"}
	inventory, err := OpenInventory(store, starter)
	if err != nil {
		t.Fatal(err)
	}
	items, err := inventory.GrantOnce("costume-burst-materials", stacks)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := OpenWallet(store, Currency{Gold: 1000})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := OpenCollectionStore(store, []Costume{{InvenIndex: 88, ID: 4202, DesignID: 9911}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewCostumeBurstService(testCostumeBurstDesign(), collection, inventory, wallet)
	if err != nil {
		t.Fatal(err)
	}
	return service, collection, inventory, wallet, items
}

func TestCostumeBurstConsumesExactSplitCostsPersistsAndReplays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	service, collection, inventory, wallet, stacks := newCostumeBurstFixture(t, path, []gamedata.BattleReward{
		{Type: 8, ID: 710, Count: 1},
		{Type: 8, ID: 710, Count: 1},
	})
	service.BeginSession("login-a")
	request := costumeBurstRequest(7, 4202, stacks[1], Item{Type: 4, Count: 100}, stacks[0])
	code, body, handled, err := service.Handle("/CostumeBurst", request)
	if err != nil || !handled || code != costumeBurstPacketCode {
		t.Fatalf("CostumeBurst code=%d handled=%v body=%x err=%v", code, handled, body, err)
	}
	level, found, err := wire.Varint(body, 1)
	if err != nil || !found || level != 1 {
		t.Fatalf("response level=%d found=%v err=%v body=%x", level, found, err, body)
	}
	got, found := collection.CostumeByID(4202)
	if !found || got.InvenIndex != 88 || got.DesignID != 9911 || got.BurstLevel != 1 {
		t.Fatalf("upgraded costume=%+v found=%v", got, found)
	}
	if wallet.Snapshot().Gold != 900 {
		t.Fatalf("gold=%d", wallet.Snapshot().Gold)
	}
	for _, stack := range stacks {
		if err := inventory.CanConsume([]Item{stack}); err == nil {
			t.Fatalf("material stack %d survived", stack.InvenIndex)
		}
	}

	code2, body2, handled, err := service.Handle("/CostumeBurst", request)
	if err != nil || !handled || code2 != code || string(body2) != string(body) {
		t.Fatalf("same-session replay code=%d handled=%v body=%x err=%v", code2, handled, body2, err)
	}
	if wallet.Snapshot().Gold != 900 || collection.Costumes()[0].BurstLevel != 1 {
		t.Fatal("same-session replay mutated state")
	}

	store := testStore(path)
	reloadedInventory, err := OpenInventory(store, &Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	reloadedWallet, err := OpenWallet(store, Currency{})
	if err != nil {
		t.Fatal(err)
	}
	reloadedCollection, err := OpenCollectionStore(store, []Costume{{InvenIndex: 88, ID: 4202, DesignID: 9911}})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewCostumeBurstService(testCostumeBurstDesign(), reloadedCollection, reloadedInventory, reloadedWallet)
	if err != nil {
		t.Fatal(err)
	}
	restarted.BeginSession("after-restart")
	code2, body2, handled, err = restarted.Handle("/CostumeBurst", request)
	if err != nil || !handled || code2 != code || string(body2) != string(body) {
		t.Fatalf("durable replay code=%d handled=%v body=%x err=%v", code2, handled, body2, err)
	}
	if reloadedWallet.Snapshot().Gold != 900 || reloadedCollection.Costumes()[0].BurstLevel != 1 {
		t.Fatal("durable replay charged or advanced again")
	}
}

func TestCostumeBurstRequiresLogicalCostumeIDAndProtectsSessionSequence(t *testing.T) {
	service, collection, inventory, wallet, stacks := newCostumeBurstFixture(t, filepath.Join(t.TempDir(), "state.json"), []gamedata.BattleReward{{Type: 8, ID: 710, Count: 4}})
	material := stacks[0]
	material.Count = 2
	for _, wrong := range []uint64{88, 9911} {
		request := costumeBurstRequest(wrong, wrong, material, Item{Type: 4, Count: 100})
		if _, _, handled, err := service.Handle("/CostumeBurst", request); err == nil || !handled {
			t.Fatalf("non-logical costume ID %d accepted: handled=%v err=%v", wrong, handled, err)
		}
	}
	service.BeginSession("login-a")
	request := costumeBurstRequest(9, 4202, material, Item{Type: 4, Count: 100})
	if _, _, _, err := service.Handle("/CostumeBurst", request); err != nil {
		t.Fatal(err)
	}
	different := costumeBurstRequest(9, 4202, material, Item{Type: 4, Count: 200})
	if _, _, handled, err := service.Handle("/CostumeBurst", different); err == nil || !handled {
		t.Fatalf("sequence reuse accepted: handled=%v err=%v", handled, err)
	}
	if collection.Costumes()[0].BurstLevel != 1 || wallet.Snapshot().Gold != 900 {
		t.Fatal("rejected sequence reuse mutated state")
	}
	remaining := material
	remaining.Count = 2
	if err := inventory.CanConsume([]Item{remaining}); err != nil {
		t.Fatalf("rejected request consumed remaining items: %v", err)
	}
}

func TestCostumeBurstRejectsInvalidMaterialsAndProtocolRanges(t *testing.T) {
	tests := []struct {
		name      string
		materials func(Item) []Item
	}{
		{"short", func(stack Item) []Item { stack.Count = 1; return []Item{stack, {Type: 4, Count: 100}} }},
		{"extra", func(stack Item) []Item {
			stack.Count = 2
			return []Item{stack, {Type: 4, Count: 100}, {InvenIndex: 900, Type: 8, ID: 711, Count: 1}}
		}},
		{"wrong-gold", func(stack Item) []Item { stack.Count = 2; return []Item{stack, {Type: 4, Count: 99}} }},
		{"duplicate-overdraw", func(stack Item) []Item { stack.Count = 1; return []Item{stack, stack, {Type: 4, Count: 100}} }},
		{"range", func(stack Item) []Item {
			stack.Count = 2
			stack.ExpiryTime = math.MaxInt64 + 1
			return []Item{stack, {Type: 4, Count: 100}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stackCount := uint64(2)
			if test.name == "duplicate-overdraw" {
				stackCount = 1
			}
			service, collection, inventory, wallet, stacks := newCostumeBurstFixture(t, filepath.Join(t.TempDir(), "state.json"), []gamedata.BattleReward{{Type: 8, ID: 710, Count: stackCount}})
			request := costumeBurstRequest(1, 4202, test.materials(stacks[0])...)
			if _, _, handled, err := service.Handle("/CostumeBurst", request); err == nil || !handled {
				t.Fatalf("invalid request accepted: handled=%v err=%v", handled, err)
			}
			if collection.Costumes()[0].BurstLevel != 0 || wallet.Snapshot().Gold != 1000 {
				t.Fatal("invalid request mutated level or wallet")
			}
			if err := inventory.CanConsume([]Item{stacks[0]}); err != nil {
				t.Fatalf("invalid request consumed material: %v", err)
			}
		})
	}
}

func TestCostumeBurstSQLiteRollbackIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repository, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	starter := &Starter{Version: "2.35.10"}
	inventory, err := OpenInventory(repository, starter)
	if err != nil {
		t.Fatal(err)
	}
	stacks, err := inventory.GrantOnce("rollback-material", []gamedata.BattleReward{{Type: 8, ID: 710, Count: 2}})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := OpenWallet(repository, Currency{Gold: 1000})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := OpenCollectionStore(repository, []Costume{{InvenIndex: 88, ID: 4202}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ensure := range []func() error{inventory.EnsurePersisted, wallet.EnsurePersisted, collection.EnsurePersisted} {
		if err := ensure(); err != nil {
			t.Fatal(err)
		}
	}
	service, _ := NewCostumeBurstService(testCostumeBurstDesign(), collection, inventory, wallet)
	request := costumeBurstRequest(1, 4202, stacks[0], Item{Type: 4, Count: 100})
	operation, err := repository.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if code, _, handled, err := service.Handle("/CostumeBurst", request); err != nil || !handled || code != costumeBurstPacketCode {
		t.Fatalf("transactional request code=%d handled=%v err=%v", code, handled, err)
	}
	if err := operation.Rollback(); err == nil {
		t.Fatal("dirty request rollback did not fence published in-memory state")
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reloadedInventory, err := OpenInventory(reopened, starter)
	if err != nil {
		t.Fatal(err)
	}
	reloadedWallet, err := OpenWallet(reopened, Currency{})
	if err != nil {
		t.Fatal(err)
	}
	reloadedCollection, err := OpenCollectionStore(reopened, []Costume{{InvenIndex: 88, ID: 4202}})
	if err != nil {
		t.Fatal(err)
	}
	if reloadedWallet.Snapshot().Gold != 1000 || reloadedCollection.Costumes()[0].BurstLevel != 0 {
		t.Fatalf("rolled back state gold=%d costume=%+v", reloadedWallet.Snapshot().Gold, reloadedCollection.Costumes()[0])
	}
	if err := reloadedInventory.CanConsume([]Item{stacks[0]}); err != nil {
		t.Fatalf("rolled back inventory did not restore materials: %v", err)
	}
}
