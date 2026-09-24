package player

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"bd2server/internal/stateio"
)

func TestCharacterEntriesPreserveOrderAndUpdateOneEntity(t *testing.T) {
	storage := &collectionWriteSpy{Memory: stateio.NewMemory()}
	inventory, err := OpenInventory(storage, &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	seed := []Character{
		{InvenIndex: 3, ID: 30, Level: 1},
		{InvenIndex: 1, ID: 10, Level: 1},
		{InvenIndex: 2, ID: 20, Level: 1},
	}
	characters, err := OpenCharacterStore(storage, seed, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := characters.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	core, err := storage.Load("characters")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(core, []byte(`"characters"`)) || !bytes.Contains(core, []byte(`"character_order":[3,1,2]`)) {
		t.Fatalf("incorrect character core: %s", core)
	}
	if storage.coreWrites != 1 || len(storage.mutations) != 1 || len(storage.mutations[0]) != 3 {
		t.Fatalf("initial writes: cores=%d mutations=%+v", storage.coreWrites, storage.mutations)
	}
	next := append([]Character(nil), characters.characters...)
	next[1].Level = 20
	if err := characters.persist(next); err != nil {
		t.Fatal(err)
	}
	characters.characters = next
	if storage.coreWrites != 1 || len(storage.mutations) != 2 || len(storage.mutations[1]) != 1 || storage.mutations[1][0].Key != "1" {
		t.Fatalf("growth rewrote unrelated characters: cores=%d mutations=%+v", storage.coreWrites, storage.mutations)
	}
	reloaded, err := OpenCharacterStore(storage, nil, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.RawAll(); !reflect.DeepEqual(got, next) {
		t.Fatalf("reloaded order or content differs: %+v", got)
	}
}

func TestCharacterEntriesRejectIncompleteOrLegacyState(t *testing.T) {
	valid, err := json.Marshal(Character{InvenIndex: 1, ID: 10, Level: 1})
	if err != nil {
		t.Fatal(err)
	}
	wrongIndex, err := json.Marshal(Character{InvenIndex: 2, ID: 10, Level: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		core    string
		entries map[string][]byte
	}{
		{"legacy array", `{"version":"2.34.13","characters":[]}`, nil},
		{"duplicate order", `{"version":"2.34.13","character_order":[1,1]}`, map[string][]byte{"1": valid, "2": wrongIndex}},
		{"missing entry", `{"version":"2.34.13","character_order":[1]}`, nil},
		{"extra entry", `{"version":"2.34.13","character_order":[]}`, map[string][]byte{"1": valid}},
		{"mismatched index", `{"version":"2.34.13","character_order":[1]}`, map[string][]byte{"1": wrongIndex}},
		{"noncanonical key", `{"version":"2.34.13","character_order":[1]}`, map[string][]byte{"01": valid}},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage := stateio.NewMemory()
			if err := storage.Save("characters", []byte(test.core)); err != nil {
				t.Fatal(err)
			}
			for key, payload := range test.entries {
				if err := storage.PutEntry("characters", "characters", key, payload); err != nil {
					t.Fatal(err)
				}
			}
			inventory, err := OpenInventory(storage, &Starter{Version: "2.34.13"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := OpenCharacterStore(storage, nil, inventory, "", ""); err == nil {
				t.Fatal("accepted invalid character state")
			}
		})
	}
}

func TestCharacterEntriesRejectOrphansWithoutCore(t *testing.T) {
	storage := stateio.NewMemory()
	if err := storage.PutEntry("characters", "characters", "1", []byte(`{"inven_index":1,"id":10,"level":1}`)); err != nil {
		t.Fatal(err)
	}
	inventory, err := OpenInventory(storage, &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCharacterStore(storage, nil, inventory, "", ""); err == nil {
		t.Fatal("accepted orphaned character entry")
	}
}
