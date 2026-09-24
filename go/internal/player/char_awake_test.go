package player

import (
	"path/filepath"
	"testing"

	"bd2server/internal/gamedata"
	"bd2server/internal/wire"
)

func newCharAwakeHarness(t *testing.T) (*CharAwakeService, *CollectionStore, *Inventory, *Wallet, []Item) {
	t.Helper()
	dir := t.TempDir()
	starter := &Starter{Version: "2.34.13"}
	inventory, err := OpenInventory(testStore(filepath.Join(dir, "items.json")), starter)
	if err != nil {
		t.Fatal(err)
	}
	granted, err := inventory.GrantOnce("awake-test", []gamedata.BattleReward{
		{Type: 8, ID: 701, Count: 1}, {Type: 8, ID: 702, Count: 1}, {Type: 8, ID: 703, Count: 1}, {Type: 8, ID: 705, Count: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := OpenWallet(testStore(filepath.Join(dir, "wallet.json")), Currency{Gold: 1000})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := OpenCollectionStore(testStore(filepath.Join(dir, "collection.json")), nil)
	if err != nil {
		t.Fatal(err)
	}
	characters := &CharacterStore{characters: []Character{{InvenIndex: 77, ID: 354, Level: 100}}}
	character := gamedata.CharAwakeCharacter{UniqueCharID: 35, Active: true}
	for i := 0; i < 3; i++ {
		character.ImprintGrowth[i] = []gamedata.CharAwakeGrowth{{
			ID: uint64(i + 1), StatType: uint64(i*2 + 1), StatValue: float64(i + 1),
			Costs: []gamedata.CharAwakeCost{{Type: 8, ID: uint64(701 + i), Count: 1}, {Type: 4, Count: 100}},
		}}
	}
	character.AwakeGrowth = []gamedata.CharAwakeGrowth{
		{ID: 100, StatType: 4, StatValue: .12, Costs: []gamedata.CharAwakeCost{{Type: 8, ID: 705, Count: 20}, {Type: 4, Count: 500}}},
		{ID: 101, StatType: 14, StatValue: .1},
	}
	design := &gamedata.CharAwakeDesign{
		Characters: map[uint64]gamedata.CharAwakeCharacter{35: character},
		Stages: map[uint64]gamedata.CharAwakeCharacterStage{
			354: {UniqueCharID: 35, Grade: 5, GrowthGrade: 5, MaximumLevel: 100},
		},
	}
	service, err := NewCharAwakeService(design, collection, characters, inventory, wallet)
	if err != nil {
		t.Fatal(err)
	}
	return service, collection, inventory, wallet, granted
}

func awakeMaterial(item Item) []byte { return ItemWire(item) }

func TestCharImprintAndAwakePersistConsumeAndRestoreInfo(t *testing.T) {
	service, collection, inventory, wallet, granted := newCharAwakeHarness(t)
	request := wire.AppendVarint(nil, 1, 10)
	request = wire.AppendVarint(request, 2, 77)
	for slot := uint64(1); slot <= 3; slot++ {
		target := wire.AppendVarint(wire.AppendVarint(nil, 1, slot), 2, 1)
		request = wire.AppendBytes(request, 3, target)
		request = wire.AppendBytes(request, 4, awakeMaterial(granted[slot-1]))
	}
	request = wire.AppendBytes(request, 4, awakeMaterial(Item{Type: 4, Count: 300}))
	code, proto, handled, err := service.Handle("/CharImprintLevelUp", request)
	if err != nil || !handled || code != 327 || len(proto) != 0 {
		t.Fatalf("imprint code=%d proto=%x handled=%v err=%v", code, proto, handled, err)
	}
	progress, found := collection.CharAwakeState(35)
	if !found || progress.ImprintLevels != [3]uint64{1, 1, 1} || progress.IsAwake {
		t.Fatalf("imprint progress=%+v found=%v", progress, found)
	}
	if wallet.Snapshot().Gold != 700 || len(inventory.All()) != 1 {
		t.Fatalf("post-imprint wallet=%+v items=%+v", wallet.Snapshot(), inventory.All())
	}

	request = wire.AppendVarint(nil, 1, 11)
	request = wire.AppendVarint(request, 2, 77)
	request = wire.AppendBytes(request, 3, awakeMaterial(granted[3]))
	request = wire.AppendBytes(request, 3, awakeMaterial(Item{Type: 4, Count: 500}))
	code, proto, handled, err = service.Handle("/CharAwakeActive", request)
	if err != nil || !handled || code != 328 || len(proto) != 0 {
		t.Fatalf("awake code=%d proto=%x handled=%v err=%v", code, proto, handled, err)
	}
	progress, _ = collection.CharAwakeState(35)
	if !progress.IsAwake || wallet.Snapshot().Gold != 200 || len(inventory.All()) != 0 {
		t.Fatalf("post-awake progress=%+v wallet=%+v items=%+v", progress, wallet.Snapshot(), inventory.All())
	}
	restarted, err := OpenCollectionStore(collection.store, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.collection = restarted
	progress, found = restarted.CharAwakeState(35)
	if !found || progress.ImprintLevels != [3]uint64{1, 1, 1} || !progress.IsAwake {
		t.Fatalf("restarted awakening progress=%+v found=%v", progress, found)
	}

	code, proto, handled, err = service.Handle("/CharAwakeInfo", wire.AppendVarint(nil, 1, 12))
	if err != nil || !handled || code != 326 {
		t.Fatalf("info code=%d handled=%v err=%v", code, handled, err)
	}
	var entry []byte
	if err := wire.Walk(proto, func(field wire.Field) error {
		if field.Number == 1 {
			entry = append([]byte(nil), field.Value...)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[int]uint64{1: 35, 2: 1, 3: 1, 4: 1, 5: 1} {
		got, present, err := wire.Varint(entry, field)
		if err != nil || !present || got != want {
			t.Fatalf("CharAwakeDBInfo field %d=%d present=%v err=%v want=%d proto=%x", field, got, present, err, want, entry)
		}
	}
}

func TestCharImprintRejectsClientCostMismatchBeforeMutation(t *testing.T) {
	service, collection, inventory, wallet, granted := newCharAwakeHarness(t)
	request := wire.AppendVarint(nil, 1, 20)
	request = wire.AppendVarint(request, 2, 77)
	request = wire.AppendBytes(request, 3, wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 1))
	bad := granted[0]
	bad.Count = 2
	request = wire.AppendBytes(request, 4, awakeMaterial(bad))
	request = wire.AppendBytes(request, 4, awakeMaterial(Item{Type: 4, Count: 100}))
	if _, _, handled, err := service.Handle("/CharImprintLevelUp", request); !handled || err == nil {
		t.Fatalf("mismatched costs handled=%v err=%v", handled, err)
	}
	if _, found := collection.CharAwakeState(35); found || wallet.Snapshot().Gold != 1000 || len(inventory.All()) != 4 {
		t.Fatalf("rejected request mutated state: found=%v wallet=%+v items=%+v", found, wallet.Snapshot(), inventory.All())
	}
}
