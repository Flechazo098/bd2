package player

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"bd2server/internal/stateio"
)

type collectionWriteSpy struct {
	*stateio.Memory
	coreWrites int
	mutations  [][]stateio.EntryMutation
}

func (s *collectionWriteSpy) SaveWithEntries(domain string, core []byte, changes []stateio.EntryMutation) error {
	if core != nil {
		s.coreWrites++
	}
	s.mutations = append(s.mutations, append([]stateio.EntryMutation(nil), changes...))
	return s.Memory.SaveWithEntries(domain, core, changes)
}

func TestCollectionPersistsOnlyChangedEntries(t *testing.T) {
	storage := &collectionWriteSpy{Memory: stateio.NewMemory()}
	collection, err := OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := collection.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	first := cloneCollection(collection.data)
	first.Characters = []Character{{InvenIndex: 920000001, ID: 6490, Level: 1}}
	first.Costumes = []Costume{{InvenIndex: 930000001, ID: 64901, UseChar: 920000001}}
	first.Grants["first"] = CollectionGrant{CharacterIndices: []uint64{920000001}, CostumeIndices: []uint64{930000001}}
	first.GachaApplied["draw:first"] = true
	if err := collection.commit(first); err != nil {
		t.Fatal(err)
	}
	core, err := storage.Load("collection")
	if err != nil {
		t.Fatal(err)
	}
	for _, bucket := range collectionEntryBuckets {
		if bytes.Contains(core, []byte(`"`+bucket+`"`)) {
			t.Fatalf("collection core still contains %s", bucket)
		}
	}
	if storage.coreWrites != 1 || len(storage.mutations[1]) != 4 {
		t.Fatalf("initial entry save: core writes=%d mutations=%+v", storage.coreWrites, storage.mutations)
	}

	second := cloneCollection(collection.data)
	second.Grants["second"] = CollectionGrant{ViewCostumeIDs: []uint64{64902}}
	if err := collection.commit(second); err != nil {
		t.Fatal(err)
	}
	if storage.coreWrites != 1 || len(storage.mutations[2]) != 1 || storage.mutations[2][0].Bucket != "grants" || storage.mutations[2][0].Key != "second" {
		t.Fatalf("entry-only update rewrote core or unrelated entries: %+v", storage.mutations[2])
	}
	loaded, err := OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Characters()) != 1 || len(loaded.Costumes()) != 1 || !loaded.data.GachaApplied["draw:first"] {
		t.Fatalf("reloaded collection lost entities or applied draw: %+v", loaded.data)
	}
	if _, found := loaded.Grant("second"); !found {
		t.Fatal("reloaded collection lost second grant")
	}
}

func TestCollectionRejectsInlineLedgerInFinalCore(t *testing.T) {
	storage := stateio.NewMemory()
	core := map[string]any{
		"version": "2.34.13", "next_character_index": 920000001,
		"next_costume_index": 930000001, "grants": map[string]any{},
	}
	encoded, err := json.Marshal(core)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Save("collection", encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCollectionStore(storage, nil); err == nil {
		t.Fatal("old inline collection ledger was accepted")
	}
}

func TestCollectionEntryBucketsRoundTripAndDelete(t *testing.T) {
	storage := stateio.NewMemory()
	collection, err := OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	next := cloneCollection(collection.data)
	next.Characters = []Character{{InvenIndex: 920000001, ID: 6490, Level: 1}}
	next.Costumes = []Costume{{InvenIndex: 930000001, ID: 64901, UseChar: 920000001}}
	next.Grants["draw:1"] = CollectionGrant{ViewCostumeIDs: []uint64{64901}}
	next.GachaApplied["draw:1"] = true
	next.GachaUsers["1"] = GachaUserState{GroupID: 1, Point: 20}
	next.GachaFixed["1:2"] = GachaFixedState{FixedID: 1, Type: 2, Count: 3}
	next.StepUpProgress["1"] = 2
	next.GachaPointExchange["exchange:1"] = GachaPointExchange{GroupID: 1, Count: 3}
	next.GachaSelections["1"] = []GachaSelection{{GroupID: 1, Slot: 1, ItemID: 64901}}
	next.CostumePotential["930000001"] = []uint64{2, 4}
	next.CharAwake["6490"] = CharAwakeProgress{ImprintLevels: [3]uint64{1, 0, 0}}
	if err := collection.commit(next); err != nil {
		t.Fatal(err)
	}
	loaded, err := OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.data, next) {
		t.Fatalf("collection entries changed across reload: loaded=%+v want=%+v", loaded.data, next)
	}
	removed := cloneCollection(loaded.data)
	delete(removed.Grants, "draw:1")
	delete(removed.GachaApplied, "draw:1")
	removed.Characters = nil
	removed.Costumes = nil
	if err := loaded.commit(removed); err != nil {
		t.Fatal(err)
	}
	for bucket, key := range map[string]string{"grants": "draw:1", "gacha_applied": "draw:1", "characters": "920000001", "costumes": "930000001"} {
		if _, found, err := storage.LoadEntry("collection", bucket, key); err != nil || found {
			t.Fatalf("deleted entry %s/%s found=%v err=%v", bucket, key, found, err)
		}
	}
}
