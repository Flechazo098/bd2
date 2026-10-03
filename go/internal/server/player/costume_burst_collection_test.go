package player

import (
	"encoding/json"
	"strings"
	"testing"

	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func testCostumeBurstRecord(costumeID, level uint64) CostumeBurstUpgradeRecord {
	return CostumeBurstUpgradeRecord{
		CostumeID: costumeID,
		Level:     level,
		Digest:    strings.Repeat("ab", 32),
		Code:      578,
		Body:      wire.AppendVarint(nil, 1, level),
	}
}

func TestCollectionCostumeBurstPersistsOverlayAndReplayForEveryCostumeSource(t *testing.T) {
	storage := &collectionWriteSpy{Memory: stateio.NewMemory()}
	base := []Costume{{InvenIndex: 11, ID: 101, BurstLevel: 0}}
	collection, err := OpenCollectionStore(storage, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := collection.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	next := cloneCollection(collection.data)
	next.Costumes = []Costume{{InvenIndex: 22, ID: 202}}
	if err := collection.commit(next); err != nil {
		t.Fatal(err)
	}

	baseRecord := testCostumeBurstRecord(101, 1)
	before := len(storage.mutations)
	if err := collection.ApplyCostumeBurst(11, 0, 1, baseRecord); err != nil {
		t.Fatal(err)
	}
	if len(storage.mutations) != before+1 || len(storage.mutations[before]) != 2 {
		t.Fatalf("burst level and replay were not one collection write: %+v", storage.mutations[before:])
	}
	if storage.mutations[before][0].Bucket != "costume_burst_levels" || storage.mutations[before][1].Bucket != "costume_burst_upgrades" {
		t.Fatalf("unexpected burst mutations: %+v", storage.mutations[before])
	}
	if err := collection.ApplyCostumeBurst(22, 0, 1, testCostumeBurstRecord(202, 1)); err != nil {
		t.Fatal(err)
	}
	if err := collection.ApplyCostumeBurst(22, 1, 2, testCostumeBurstRecord(202, 2)); err != nil {
		t.Fatal(err)
	}

	if got, found := collection.CostumeByID(101); !found || got.InvenIndex != 11 || got.BurstLevel != 1 {
		t.Fatalf("base costume overlay=%+v found=%v", got, found)
	}
	if got, found := collection.CostumeByIndex(22); !found || got.ID != 202 || got.BurstLevel != 2 {
		t.Fatalf("collection costume overlay=%+v found=%v", got, found)
	}
	replay, found := collection.CostumeBurstReplay(22, 2)
	if !found || replay.CostumeID != 202 || replay.Level != 2 || replay.Code != 578 {
		t.Fatalf("replay=%+v found=%v", replay, found)
	}
	replay.Body[0] = 0
	if again, _ := collection.CostumeBurstReplay(22, 2); len(again.Body) == 0 || again.Body[0] == 0 {
		t.Fatal("CostumeBurstReplay returned an aliased response body")
	}

	core, err := storage.Load("collection")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(core), "costume_burst") {
		t.Fatalf("burst entries leaked into collection core: %s", core)
	}
	restarted, err := OpenCollectionStore(storage, base)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := restarted.CostumeByIndex(11); got.BurstLevel != 1 {
		t.Fatalf("restarted base burst level=%d", got.BurstLevel)
	}
	if got, _ := restarted.CostumeByID(202); got.BurstLevel != 2 {
		t.Fatalf("restarted collection burst level=%d", got.BurstLevel)
	}
	if got, ok := restarted.CostumeBurstReplay(22, 1); !ok || got.Level != 1 {
		t.Fatalf("restarted first replay=%+v found=%v", got, ok)
	}
}

func TestCollectionCostumeBurstCASAndRecordValidation(t *testing.T) {
	collection, err := OpenCollectionStore(stateio.NewMemory(), []Costume{{InvenIndex: 11, ID: 101}})
	if err != nil {
		t.Fatal(err)
	}
	record := testCostumeBurstRecord(101, 1)
	if err := collection.ApplyCostumeBurst(11, 0, 1, record); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"stale current":    func() error { return collection.ApplyCostumeBurst(11, 0, 1, record) },
		"skipped level":    func() error { return collection.ApplyCostumeBurst(11, 1, 3, testCostumeBurstRecord(101, 3)) },
		"wrong costume":    func() error { return collection.ApplyCostumeBurst(11, 1, 2, testCostumeBurstRecord(102, 2)) },
		"unknown instance": func() error { return collection.ApplyCostumeBurst(99, 0, 1, record) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("invalid costume burst transition was accepted")
			}
		})
	}
	if got, _ := collection.CostumeByIndex(11); got.BurstLevel != 1 {
		t.Fatalf("rejected transitions mutated level to %d", got.BurstLevel)
	}
	if _, found := collection.CostumeBurstReplay(11, 2); found {
		t.Fatal("rejected transition wrote a replay")
	}
}

func TestCollectionCostumeByIDRejectsAmbiguousOwnership(t *testing.T) {
	collection, err := OpenCollectionStore(stateio.NewMemory(), []Costume{{InvenIndex: 11, ID: 101}, {InvenIndex: 12, ID: 101}})
	if err != nil {
		t.Fatal(err)
	}
	if costume, found := collection.CostumeByID(101); found || costume.InvenIndex != 0 || costume.ID != 0 {
		t.Fatalf("ambiguous logical ID resolved to %+v", costume)
	}
}

func TestCollectionRejectsMalformedCostumeBurstEntries(t *testing.T) {
	validRecord, err := json.Marshal(testCostumeBurstRecord(101, 1))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		bucket  string
		key     string
		payload []byte
		seed    Costume
		prepare func(*stateio.Memory)
	}{
		{name: "noncanonical level key", bucket: "costume_burst_levels", key: "011", payload: []byte("1"), seed: Costume{InvenIndex: 11, ID: 101}},
		{name: "orphan level", bucket: "costume_burst_levels", key: "99", payload: []byte("1"), seed: Costume{InvenIndex: 11, ID: 101}},
		{name: "zero level", bucket: "costume_burst_levels", key: "11", payload: []byte("0"), seed: Costume{InvenIndex: 11, ID: 101}},
		{name: "level decreases seed", bucket: "costume_burst_levels", key: "11", payload: []byte("1"), seed: Costume{InvenIndex: 11, ID: 101, BurstLevel: 2}},
		{name: "noncanonical replay key", bucket: "costume_burst_upgrades", key: "011:1", payload: validRecord, seed: Costume{InvenIndex: 11, ID: 101}, prepare: func(s *stateio.Memory) { _ = s.PutEntry("collection", "costume_burst_levels", "11", []byte("1")) }},
		{name: "replay above current", bucket: "costume_burst_upgrades", key: "11:1", payload: validRecord, seed: Costume{InvenIndex: 11, ID: 101}},
		{name: "invalid replay digest", bucket: "costume_burst_upgrades", key: "11:1", payload: func() []byte {
			r := testCostumeBurstRecord(101, 1)
			r.Digest = strings.Repeat("zz", 32)
			b, _ := json.Marshal(r)
			return b
		}(), seed: Costume{InvenIndex: 11, ID: 101}, prepare: func(s *stateio.Memory) { _ = s.PutEntry("collection", "costume_burst_levels", "11", []byte("1")) }},
		{name: "mismatched response", bucket: "costume_burst_upgrades", key: "11:1", payload: func() []byte {
			r := testCostumeBurstRecord(101, 1)
			r.Body = wire.AppendVarint(nil, 1, 2)
			b, _ := json.Marshal(r)
			return b
		}(), seed: Costume{InvenIndex: 11, ID: 101}, prepare: func(s *stateio.Memory) { _ = s.PutEntry("collection", "costume_burst_levels", "11", []byte("1")) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storage := stateio.NewMemory()
			collection, err := OpenCollectionStore(storage, []Costume{test.seed})
			if err != nil {
				t.Fatal(err)
			}
			if err := collection.EnsurePersisted(); err != nil {
				t.Fatal(err)
			}
			if test.prepare != nil {
				test.prepare(storage)
			}
			if err := storage.PutEntry("collection", test.bucket, test.key, test.payload); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenCollectionStore(storage, []Costume{test.seed}); err == nil {
				t.Fatal("malformed costume burst storage was accepted")
			}
		})
	}
}
