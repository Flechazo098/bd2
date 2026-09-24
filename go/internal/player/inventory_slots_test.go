package player

import (
	"os"
	"path/filepath"
	"testing"

	"bd2server/internal/gamedata"
	"bd2server/internal/stateio"
	"bd2server/internal/wire"
)

func inventorySlotTestDesign() *gamedata.InventorySlotDesign {
	return &gamedata.InventorySlotDesign{
		Items:            gamedata.InventorySlotRule{Default: 100, Maximum: 500, PriceType: 4, BasePrice: 300, MaxPrice: 10000},
		Storage:          gamedata.InventorySlotRule{Default: 100, Maximum: 400, PriceType: 4, BasePrice: 300, MaxPrice: 10000},
		Equipment:        gamedata.InventorySlotRule{Default: 500, Maximum: 2000, PriceType: 4, BasePrice: 300, MaxPrice: 10000},
		EquipmentStorage: gamedata.InventorySlotRule{Default: 100, Maximum: 400, PriceType: 4, BasePrice: 300, MaxPrice: 10000},
	}
}

func inventorySlotTestCounts() InventorySlotCounts {
	return InventorySlotCounts{Items: 100, Storage: 100, Equipment: 500, EquipmentStorage: 100}
}

func TestInventorySlotExpansionChargesPersistsAndReplays(t *testing.T) {
	store := stateio.NewMemory()
	wallet, err := OpenWallet(store, Currency{Gold: 100000})
	if err != nil {
		t.Fatal(err)
	}
	slots, err := OpenInventorySlots(store, inventorySlotTestDesign(), inventorySlotTestCounts(), wallet)
	if err != nil {
		t.Fatal(err)
	}
	slots.BeginSession("test")
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 7), 2, 2)
	code, response, ok, err := slots.Handle("/InvenAddSlot", request)
	if err != nil || !ok || code != 24 || len(response) != 0 {
		t.Fatalf("code=%d response=%x ok=%t err=%v", code, response, ok, err)
	}
	if wallet.Snapshot().Gold != 99100 { // first two slots cost 400 + 500
		t.Fatalf("gold=%d", wallet.Snapshot().Gold)
	}
	if _, _, _, err := slots.Handle("/InvenAddSlot", request); err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().Gold != 99100 {
		t.Fatalf("replay charged twice: %d", wallet.Snapshot().Gold)
	}
	reopened, err := OpenInventorySlots(store, inventorySlotTestDesign(), inventorySlotTestCounts(), wallet)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := reopened.InventorySlotCounts()
	if err != nil || counts.Items != 102 {
		t.Fatalf("counts=%+v err=%v", counts, err)
	}
}

func TestInventorySlotEndpointsUseTheirProtocolCodes(t *testing.T) {
	for path, code := range map[string]int{
		"/InvenAddSlot": 24, "/StorageAddSlot": 25, "/EquipAddSlot": 39, "/EquipStorageAddSlot": 82,
	} {
		t.Run(path, func(t *testing.T) {
			store := stateio.NewMemory()
			wallet, _ := OpenWallet(store, Currency{Gold: 1000})
			slots, _ := OpenInventorySlots(store, inventorySlotTestDesign(), inventorySlotTestCounts(), wallet)
			slots.BeginSession("test")
			request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 1)
			got, body, ok, err := slots.Handle(path, request)
			if err != nil || !ok || got != code || len(body) != 0 || wallet.Snapshot().Gold != 600 {
				t.Fatalf("code=%d body=%x ok=%t gold=%d err=%v", got, body, ok, wallet.Snapshot().Gold, err)
			}
		})
	}
}

func TestUnlimitedInventoryUsesGameDataMaximumAtLoginOnly(t *testing.T) {
	store := stateio.NewMemory()
	wallet, _ := OpenWallet(store, Currency{})
	slots, _ := OpenInventorySlots(store, inventorySlotTestDesign(), inventorySlotTestCounts(), wallet)
	path := filepath.Join(t.TempDir(), "dev-tools.json")
	slots.AttachDevelopmentSettings(path)
	if err := os.WriteFile(path, []byte(`{"version":1,"inventory":{"unlimited":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	counts, err := slots.InventorySlotCounts()
	if err != nil {
		t.Fatal(err)
	}
	if counts.Items != 500 || counts.Equipment != 2000 || counts.Storage != 100 || counts.EquipmentStorage != 100 {
		t.Fatalf("counts=%+v", counts)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"inventory":{"unlimited":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	counts, err = slots.InventorySlotCounts()
	if err != nil || counts != inventorySlotTestCounts() {
		t.Fatalf("restored counts=%+v err=%v", counts, err)
	}
}

func TestUnlimitedInventoryRejectsMalformedDevelopmentSettings(t *testing.T) {
	store := stateio.NewMemory()
	wallet, _ := OpenWallet(store, Currency{})
	slots, _ := OpenInventorySlots(store, inventorySlotTestDesign(), inventorySlotTestCounts(), wallet)
	path := filepath.Join(t.TempDir(), "dev-tools.json")
	slots.AttachDevelopmentSettings(path)
	if err := os.WriteFile(path, []byte(`{"version":1,"inventory":{"unlimited":true},"extra":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := slots.InventorySlotCounts(); err == nil {
		t.Fatal("malformed settings accepted")
	}
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := slots.InventorySlotCounts(); err == nil {
		t.Fatal("settings without inventory accepted")
	}
}

func TestInventorySlotPriceCapsEachAddedSlot(t *testing.T) {
	rule := gamedata.InventorySlotRule{Default: 100, Maximum: 500, PriceType: 4, BasePrice: 300, MaxPrice: 10000}
	price, err := inventorySlotPrice(rule, 196, 2)
	if err != nil || price != 20000 {
		t.Fatalf("price=%d err=%v", price, err)
	}
}
