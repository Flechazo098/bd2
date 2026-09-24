package player

import (
	"encoding/binary"
	"path/filepath"
	"testing"

	"bd2server/internal/gamedata"
	"bd2server/internal/wire"
)

func TestCostumeNodeActivationSupportsSingleAndOneClickSets(t *testing.T) {
	dir := t.TempDir()
	inventory, err := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	materials, err := inventory.GrantOnce("potential", []gamedata.BattleReward{{Type: 8, ID: 114, Count: 5}})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := OpenWallet(testStore(filepath.Join(dir, "wallet.json")), Currency{Gold: 300})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(testStore(filepath.Join(dir, "characters.json")), []Character{{InvenIndex: 77, ID: 6514, Level: 100}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	collection, err := OpenCollectionStore(testStore(filepath.Join(dir, "collection.json")), nil)
	if err != nil {
		t.Fatal(err)
	}
	nextCollection := cloneCollection(collection.data)
	nextCollection.Costumes = []Costume{{InvenIndex: 88, ID: 65103, UseChar: 77}}
	if err := collection.commit(nextCollection); err != nil {
		t.Fatal(err)
	}
	design := &gamedata.CostumePotentialDesign{
		Nodes: map[uint64]map[uint64]gamedata.CostumePotentialNode{65103: {
			1: {ID: 1, Costs: []gamedata.CostumePotentialCost{{Type: 4, Count: 100}}},
			2: {ID: 2, Prerequisites: []uint64{1}, Costs: []gamedata.CostumePotentialCost{{Type: 8, ID: 114, Count: 2}}},
			3: {ID: 3, Prerequisites: []uint64{2}, Costs: []gamedata.CostumePotentialCost{{Type: 8, ID: 114, Count: 3}, {Type: 4, Count: 200}}},
		}},
		CostumeUnique:   map[uint64]uint64{65103: 651},
		CharacterGrade:  map[uint64]uint64{6514: 5},
		CharacterUnique: map[uint64]uint64{6514: 651},
	}
	service, err := NewCostumePotentialService(design, collection, characters, inventory, wallet)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 77), 3, 88)
	request = wire.AppendVarint(request, 4, 1)
	request = wire.AppendBytes(request, 5, ItemWire(Item{Type: 4, Count: 100}))
	code, response, handled, err := service.Handle("/CostumeNodeActivation", request)
	if err != nil || !handled || code != 261 || len(response) != 0 {
		t.Fatalf("single activation code=%d handled=%v response=%x err=%v", code, handled, response, err)
	}
	oneClick := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, 77), 3, 88)
	oneClick = wire.AppendVarint(wire.AppendVarint(oneClick, 4, 2), 4, 3)
	item := materials[0]
	oneClick = wire.AppendBytes(oneClick, 5, ItemWire(item))
	oneClick = wire.AppendBytes(oneClick, 5, ItemWire(Item{Type: 4, Count: 200}))
	code, _, handled, err = service.Handle("/CostumeNodeActivation", oneClick)
	if err != nil || !handled || code != 261 {
		t.Fatalf("one-click activation code=%d handled=%v err=%v", code, handled, err)
	}
	if wallet.Snapshot().Gold != 0 {
		t.Fatalf("gold=%d", wallet.Snapshot().Gold)
	}
	costume, found := collection.CostumeByIndex(88)
	if !found || len(costume.PotentialIDs) != 3 || costume.PotentialIDs[0] != 1 || costume.PotentialIDs[2] != 3 {
		t.Fatalf("activated costume=%+v found=%v", costume, found)
	}
	var encoded []uint64
	if err := wire.Walk(CostumeWire(costume), func(field wire.Field) error {
		if field.Number == 8 {
			v, _ := binary.Uvarint(field.Value)
			encoded = append(encoded, v)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(encoded) != 3 {
		t.Fatalf("wire potential IDs=%v", encoded)
	}
	if _, _, _, err := service.Handle("/CostumeNodeActivation", oneClick); err == nil {
		t.Fatal("already active nodes accepted")
	}
	if wallet.Snapshot().Gold != 0 {
		t.Fatal("duplicate request charged wallet")
	}
}
