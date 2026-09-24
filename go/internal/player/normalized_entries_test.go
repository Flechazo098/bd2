package player

import (
	"bytes"
	"testing"

	"bd2server/internal/gamedata"
	"bd2server/internal/stateio"
)

func TestWalletLedgerUsesEntries(t *testing.T) {
	store := stateio.NewMemory()
	wallet, err := OpenWallet(store, Currency{Gold: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wallet.SpendGoldOnce("upgrade:1", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := wallet.GrantQuestOnce("quest:1", []gamedata.Reward{{Type: 4, Count: 20}}); err != nil {
		t.Fatal(err)
	}
	core, err := store.Load("wallet")
	if err != nil || bytes.Contains(core, []byte(`"granted"`)) || bytes.Contains(core, []byte(`"spent"`)) {
		t.Fatalf("wallet core contains ledger: %s, %v", core, err)
	}
	for _, bucket := range []string{"granted", "spent"} {
		entries, err := store.ListEntries("wallet", bucket)
		if err != nil || len(entries) != 1 {
			t.Fatalf("%s entries=%v: %v", bucket, entries, err)
		}
	}
	reopened, err := OpenWallet(store, Currency{})
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.WasSpent("upgrade:1") || !reopened.WasGranted("quest:1") || reopened.Snapshot().Gold != 110 {
		t.Fatalf("reopened wallet=%+v", reopened.Snapshot())
	}
}

func TestInventoryEntitiesAndGrantLedgerUseEntries(t *testing.T) {
	store := stateio.NewMemory()
	starter := &Starter{Version: "2.34.13"}
	inventory, err := OpenInventory(store, starter)
	if err != nil {
		t.Fatal(err)
	}
	items, err := inventory.GrantOnce("battle:1", []gamedata.BattleReward{{Type: 8, ID: 21, Count: 3}})
	if err != nil || len(items) != 1 {
		t.Fatalf("grant=%v: %v", items, err)
	}
	core, err := store.Load("items")
	if err != nil || bytes.Contains(core, []byte(`"items"`)) || bytes.Contains(core, []byte(`"granted"`)) || bytes.Contains(core, []byte(`"grant_items"`)) {
		t.Fatalf("items core contains entries: %s, %v", core, err)
	}
	for _, bucket := range []string{"items", "granted", "grant_items"} {
		entries, err := store.ListEntries("items", bucket)
		if err != nil || len(entries) != 1 {
			t.Fatalf("%s entries=%v: %v", bucket, entries, err)
		}
	}
	reopened, err := OpenInventory(store, starter)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.GrantedItems("battle:1"); len(got) != 1 || got[0].InvenIndex != items[0].InvenIndex {
		t.Fatalf("reopened granted items=%+v", got)
	}
}

func TestEquipmentEntitiesAndGrantLedgerUseEntries(t *testing.T) {
	store := stateio.NewMemory()
	equipment, err := OpenEquipmentInventory(store)
	if err != nil {
		t.Fatal(err)
	}
	first, err := equipment.GrantOnce("quest:1", 943035)
	if err != nil {
		t.Fatal(err)
	}
	core, err := store.Load("equipment")
	if err != nil || bytes.Contains(core, []byte(`"equipment"`)) || bytes.Contains(core, []byte(`"granted"`)) {
		t.Fatalf("equipment core contains entries: %s, %v", core, err)
	}
	for _, bucket := range []string{"equipment", "granted"} {
		entries, err := store.ListEntries("equipment", bucket)
		if err != nil || len(entries) != 1 {
			t.Fatalf("%s entries=%v: %v", bucket, entries, err)
		}
	}
	reopened, err := OpenEquipmentInventory(store)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := reopened.Granted("quest:1"); !ok || got.InvenIndex != first.InvenIndex {
		t.Fatalf("reopened grant=%+v, %t", got, ok)
	}
}

func TestEmptyInventoryAndEquipmentEnsureCore(t *testing.T) {
	store := stateio.NewMemory()
	inventory, err := OpenInventory(store, &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	if err := inventory.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	if core, err := store.Load("items"); err != nil || core == nil {
		t.Fatalf("items core=%q: %v", core, err)
	}
	equipment, err := OpenEquipmentInventory(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := equipment.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	if core, err := store.Load("equipment"); err != nil || core == nil {
		t.Fatalf("equipment core=%q: %v", core, err)
	}
}

func TestInlineLedgersAndEntitiesRejected(t *testing.T) {
	tests := []struct {
		name string
		core []byte
		open func(*stateio.Memory) error
	}{
		{"wallet", []byte(`{"version":"2.34.13","equip_mileage":0,"equip_mileage_exchange_gage":0,"granted":{}}`), func(s *stateio.Memory) error { _, err := OpenWallet(s, Currency{}); return err }},
		{"items", []byte(`{"version":"2.34.13","next_index":900000001,"items":[]}`), func(s *stateio.Memory) error { _, err := OpenInventory(s, &Starter{Version: "2.34.13"}); return err }},
		{"equipment", []byte(`{"version":"2.34.13","next_index":910000001,"equipment":[]}`), func(s *stateio.Memory) error { _, err := OpenEquipmentInventory(s); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := stateio.NewMemory()
			if err := store.Save(tt.name, tt.core); err != nil {
				t.Fatal(err)
			}
			if err := tt.open(store); err == nil {
				t.Fatal("accepted inline old-format state")
			}
		})
	}
}
