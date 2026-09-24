package player

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"bd2server/internal/gamedata"
	"bd2server/internal/wire"
)

func TestEquipMakingAgainstInstalledCurrentVersion(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("set BD2_REAL_GAMEDATA for installed GameData integration test")
	}
	dir := t.TempDir()
	inventory, err := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	materials, err := inventory.GrantOnce("making-materials", []gamedata.BattleReward{{Type: 8, ID: 204, Count: 3}, {Type: 8, ID: 201, Count: 3}})
	if err != nil || len(materials) != 2 {
		t.Fatalf("materials=%+v err=%v", materials, err)
	}
	wallet, err := OpenWallet(testStore(filepath.Join(dir, "wallet.json")), Currency{Catalyst: 10})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(testStore(filepath.Join(dir, "characters.json")), []Character{{InvenIndex: 77, ID: 140, Level: 1, TalentLevel: 1}}, inventory, root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	equipment, err := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	if err != nil {
		t.Fatal(err)
	}
	craft, err := gamedata.LoadEquipmentCraftDesign(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	upgrade, err := gamedata.LoadEquipmentUpgradeDesign(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	if err := equipment.AttachUpgrade(upgrade, wallet, inventory); err != nil {
		t.Fatal(err)
	}
	if err := equipment.AttachCraft(craft); err != nil {
		t.Fatal(err)
	}
	if err := equipment.AttachCharacters(characters); err != nil {
		t.Fatal(err)
	}
	equipment.BeginSession("making-test")
	request := wire.AppendVarint(nil, 1, 5)
	request = wire.AppendVarint(request, 2, 77)
	request = wire.AppendVarint(request, 3, 1)
	request = wire.AppendVarint(request, 4, 1)
	for _, item := range materials {
		request = wire.AppendBytes(request, 5, ItemWire(item))
	}
	code, response, handled, err := equipment.Handle("/EquipMaking", request)
	if err != nil || !handled || code != 50 {
		t.Fatalf("making code=%d handled=%t response=%x err=%v", code, handled, response, err)
	}
	if len(equipment.All()) != 1 || len(inventory.All()) != 0 {
		t.Fatalf("making equipment=%+v inventory=%+v", equipment.All(), inventory.All())
	}
	if wallet.Snapshot().Catalyst != 7 {
		t.Fatalf("making catalyst=%d", wallet.Snapshot().Catalyst)
	}
	if character, ok := characters.Find(77); !ok || character.TalentExp != 14 {
		t.Fatalf("making character=%+v found=%t", character, ok)
	}
	replayCode, replay, _, err := equipment.Handle("/EquipMaking", request)
	if err != nil || replayCode != code || string(replay) != string(response) || len(equipment.All()) != 1 || wallet.Snapshot().Catalyst != 7 {
		t.Fatalf("making replay code=%d response=%x equipment=%+v catalyst=%d err=%v", replayCode, replay, equipment.All(), wallet.Snapshot().Catalyst, err)
	}
	autoMaterials, err := inventory.GrantOnce("making-auto-materials", []gamedata.BattleReward{{Type: 8, ID: 204, Count: 3}, {Type: 8, ID: 201, Count: 3}})
	if err != nil || len(autoMaterials) != 2 {
		t.Fatalf("auto materials=%+v err=%v", autoMaterials, err)
	}
	autoRequest := wire.AppendVarint(nil, 1, 6)
	autoRequest = wire.AppendVarint(autoRequest, 2, 77)
	autoRequest = wire.AppendVarint(autoRequest, 3, 1)
	autoRequest = wire.AppendVarint(autoRequest, 4, 1)
	for _, item := range autoMaterials {
		autoRequest = wire.AppendBytes(autoRequest, 5, ItemWire(item))
	}
	autoCode, autoResponse, autoHandled, err := equipment.Handle("/EquipMakingToBreakAuto", autoRequest)
	if err != nil || !autoHandled || autoCode != 515 {
		t.Fatalf("making auto code=%d handled=%t response=%x err=%v", autoCode, autoHandled, autoResponse, err)
	}
	if _, found, _ := wire.Bytes(autoResponse, 2); !found {
		t.Fatalf("making auto omitted source equipment: %x", autoResponse)
	}
	if _, found, _ := wire.Bytes(autoResponse, 8); !found || len(equipment.All()) != 1 || len(inventory.All()) == 0 || wallet.Snapshot().Catalyst != 4 {
		t.Fatalf("making auto response=%x equipment=%+v inventory=%+v catalyst=%d", autoResponse, equipment.All(), inventory.All(), wallet.Snapshot().Catalyst)
	}
}

func TestEquipBreakUsesLevelRewardAndReplays(t *testing.T) {
	dir := t.TempDir()
	inventory, err := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := OpenWallet(testStore(filepath.Join(dir, "wallet.json")), Currency{Gold: 100})
	if err != nil {
		t.Fatal(err)
	}
	equipment, err := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	if err != nil {
		t.Fatal(err)
	}
	design := equipmentBreakTestDesign()
	if err := equipment.AttachUpgrade(design, wallet, inventory); err != nil {
		t.Fatal(err)
	}
	equipment.BeginSession("break-test")
	entry, err := equipment.GrantGeneratedOnce("break-source", Equipment{ID: 10, Level: 1, Rank: []uint64{0, 0, 0}})
	if err != nil {
		t.Fatal(err)
	}
	var packed [10]byte
	n := binary.PutUvarint(packed[:], entry.InvenIndex)
	request := wire.AppendBytes(wire.AppendVarint(nil, 1, 7), 2, packed[:n])
	code, response, handled, err := equipment.Handle("/EquipBreak", request)
	if err != nil || !handled || code != 56 || len(response) == 0 {
		t.Fatalf("break code=%d handled=%t response=%x err=%v", code, handled, response, err)
	}
	if len(equipment.All()) != 0 {
		t.Fatalf("broken equipment remained: %+v", equipment.All())
	}
	if got := inventory.All(); len(got) != 1 || got[0].ID != 201 || got[0].Type != 8 || got[0].Count != 2 {
		t.Fatalf("break inventory=%+v", got)
	}
	replayCode, replay, _, err := equipment.Handle("/EquipBreak", request)
	if err != nil || replayCode != code || string(replay) != string(response) || len(inventory.All()) != 1 {
		t.Fatalf("break replay code=%d response=%x inventory=%+v err=%v", replayCode, replay, inventory.All(), err)
	}
}

func TestEquipUpgradeToBreakAutoUpgradesThenRemoves(t *testing.T) {
	dir := t.TempDir()
	inventory, err := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := OpenWallet(testStore(filepath.Join(dir, "wallet.json")), Currency{Gold: 100})
	if err != nil {
		t.Fatal(err)
	}
	equipment, err := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	if err != nil {
		t.Fatal(err)
	}
	design := equipmentBreakTestDesign()
	if err := equipment.AttachUpgrade(design, wallet, inventory); err != nil {
		t.Fatal(err)
	}
	equipment.BeginSession("upgrade-break-test")
	entry, err := equipment.GrantOnce("upgrade-break-source", 10)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 9)
	request = wire.AppendVarint(request, 2, entry.InvenIndex)
	request = wire.AppendVarint(request, 3, 2)
	code, response, handled, err := equipment.Handle("/EquipUpgradeToBreakAuto", request)
	if err != nil || !handled || code != 516 {
		t.Fatalf("auto code=%d handled=%t response=%x err=%v", code, handled, response, err)
	}
	if attempts, _, _ := wire.Varint(response, 3); attempts != 2 {
		t.Fatalf("auto attempts=%d response=%x", attempts, response)
	}
	if used, _, _ := wire.Varint(response, 6); used != 30 || wallet.Snapshot().Gold != 70 {
		t.Fatalf("auto used=%d wallet=%+v", used, wallet.Snapshot())
	}
	if len(equipment.All()) != 0 {
		t.Fatalf("auto-broken equipment remained: %+v", equipment.All())
	}
	if got := inventory.All(); len(got) != 1 || got[0].ID != 202 || got[0].Count != 3 {
		t.Fatalf("auto break inventory=%+v", got)
	}
	replayCode, replay, _, err := equipment.Handle("/EquipUpgradeToBreakAuto", request)
	if err != nil || replayCode != code || string(replay) != string(response) || wallet.Snapshot().Gold != 70 || len(inventory.All()) != 1 {
		t.Fatalf("auto replay code=%d response=%x wallet=%+v inventory=%+v err=%v", replayCode, replay, wallet.Snapshot(), inventory.All(), err)
	}
}

func equipmentBreakTestDesign() *gamedata.EquipmentUpgradeDesign {
	return &gamedata.EquipmentUpgradeDesign{
		MaxLevel: map[uint64]uint64{10: 2}, Group: map[uint64]uint64{10: 20}, RankGroup: map[uint64]uint64{10: 30},
		Levels: map[[2]uint64]gamedata.EquipmentUpgradeLevel{
			{20, 0}: {Level: 0, Costs: []gamedata.PromotionCost{{Type: 4, Count: 10}}, SuccessRatio: 1},
			{20, 1}: {Level: 1, Costs: []gamedata.PromotionCost{{Type: 4, Count: 20}}, SuccessRatio: 1},
		},
		Break: map[[2]uint64][]gamedata.BattleReward{
			{20, 0}: {{Type: 8, ID: 200, Count: 1}},
			{20, 1}: {{Type: 8, ID: 201, Count: 2}},
			{20, 2}: {{Type: 8, ID: 202, Count: 3}},
		},
		NotTrash: map[uint64]bool{10: true},
	}
}
