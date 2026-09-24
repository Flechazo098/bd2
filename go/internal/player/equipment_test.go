package player

import (
	"encoding/binary"
	"path/filepath"
	"testing"

	"bd2server/internal/gamedata"
	"bd2server/internal/wire"
)

func TestEquipmentUpgradeAndSequenceUseGameDataCosts(t *testing.T) {
	dir := t.TempDir()
	wallet, err := OpenWallet(testStore(filepath.Join(dir, "wallet.json")), Currency{Gold: 1000})
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	if err != nil {
		t.Fatal(err)
	}
	design := &gamedata.EquipmentUpgradeDesign{
		MaxLevel: map[uint64]uint64{943035: 3}, Group: map[uint64]uint64{943035: 954},
		RankGroup: map[uint64]uint64{943035: 904}, RankRatio: map[[2]uint64][]float64{{904, 1}: {1, 0, 0, 0}},
		Levels: map[[2]uint64]gamedata.EquipmentUpgradeLevel{
			{954, 0}: {Level: 0, Costs: []gamedata.PromotionCost{{Type: 4, Count: 100}}, SuccessRatio: 1},
			{954, 1}: {Level: 1, Costs: []gamedata.PromotionCost{{Type: 4, Count: 200}}, SuccessRatio: 1},
			{954, 2}: {Level: 2, Costs: []gamedata.PromotionCost{{Type: 4, Count: 300}}, SuccessRatio: 1},
		},
	}
	items, err := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AttachUpgrade(design, wallet, items); err != nil {
		t.Fatal(err)
	}
	entry, err := store.GrantOnce("upgrade", 943035)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, entry.InvenIndex)
	request = wire.AppendBytes(request, 3, ItemWire(Item{Type: 4, Count: 100}))
	code, response, handled, err := store.Handle("/EquipUpgrade", request)
	if err != nil || !handled || code != 37 {
		t.Fatalf("single upgrade code=%d handled=%v err=%v", code, handled, err)
	}
	encoded, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("single upgrade equipment: %v", err)
	}
	base, _, _ := wire.Bytes(encoded, 5)
	if level, _, _ := wire.Varint(base, 2); level != 1 {
		t.Fatalf("single upgraded level=%d", level)
	}
	sequence := wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, entry.InvenIndex)
	sequence = wire.AppendVarint(sequence, 3, 9)
	sequence = wire.AppendVarint(sequence, 6, 3)
	code, response, handled, err = store.Handle("/EquipSequenceUpgrade", sequence)
	if err != nil || !handled || code != 176 {
		t.Fatalf("sequence upgrade code=%d handled=%v err=%v", code, handled, err)
	}
	if result, _, _ := wire.Varint(response, 3); result != equipUpgradeStopMaxLevel {
		t.Fatalf("sequence result=%d", result)
	}
	if attempts, _, _ := wire.Varint(response, 4); attempts != 2 {
		t.Fatalf("sequence attempts=%d", attempts)
	}
	if used, _, _ := wire.Varint(response, 7); used != 500 {
		t.Fatalf("sequence used gold=%d", used)
	}
	if wallet.Snapshot().Gold != 400 {
		t.Fatalf("wallet gold=%d", wallet.Snapshot().Gold)
	}
	restored, err := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.All(); len(got) != 1 || got[0].Level != 3 || got[0].UpgradeAttempts != 3 {
		t.Fatalf("persisted upgrade=%+v", got)
	}
	if rank := restored.All()[0].Rank; len(rank) != 3 || rank[0] != 1 || rank[1] != 0 || rank[2] != 0 {
		t.Fatalf("+3 should roll only the first rank: %v", rank)
	}
}

func TestEquipmentSmeltingImprovesByTotalScoreAndReplaysWithoutSecondCharge(t *testing.T) {
	dir := t.TempDir()
	wallet, err := OpenWallet(testStore(filepath.Join(dir, "wallet.json")), Currency{Gold: 1000, EquipMileageExchangeGage: 990})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	materials, err := inventory.GrantOnce("refine-material", []gamedata.BattleReward{{Type: 8, ID: 10, Count: 100}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	if err != nil {
		t.Fatal(err)
	}
	design := smeltingTestDesign([][]float64{{1, 0, 0, 0}, {0, 0, 0, 1}, {0, 0, 0, 1}})
	if err := store.AttachSmelting(design, wallet, inventory); err != nil {
		t.Fatal(err)
	}
	store.BeginSession("smelting-test")
	entry, err := store.GrantOnce("refinable", 943035)
	if err != nil {
		t.Fatal(err)
	}
	store.owned.Equipment[0].Level = 9
	store.owned.Equipment[0].Rank = []uint64{4, 1, 1} // 6 -> candidate 1+4+4=9.
	if err := store.commitLocked(cloneEquipmentSnapshot(store.owned), "test setup"); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 77), 2, entry.InvenIndex)
	request = wire.AppendBytes(request, 3, ItemWire(Item{Type: 4, Count: 80}))
	material := materials[0]
	material.Count = 30
	request = wire.AppendBytes(request, 3, ItemWire(material))
	code, response, handled, err := store.Handle("/EquipSmelting", request)
	if err != nil || !handled || code != 105 {
		t.Fatalf("smelting code=%d handled=%v err=%v", code, handled, err)
	}
	if result, found, _ := wire.Varint(response, 3); found || result != 0 {
		t.Fatalf("successful smelting result=%d found=%v", result, found)
	}
	if got := store.All()[0].Rank; len(got) != 3 || got[0] != 1 || got[1] != 4 || got[2] != 4 {
		t.Fatalf("smelting rank=%v", got)
	}
	if gauge, _, _ := wire.Varint(response, 5); gauge != 20 {
		t.Fatalf("smelting gauge=%d", gauge)
	}
	bundle, found, err := wire.Bytes(response, 6)
	if err != nil || !found {
		t.Fatalf("smelting mileage bundle: %v", err)
	}
	reward, found, err := wire.Bytes(bundle, 1)
	if err != nil || !found {
		t.Fatalf("smelting mileage item: %v", err)
	}
	if typ, _, _ := wire.Varint(reward, 3); typ != 68 {
		t.Fatalf("smelting mileage type=%d", typ)
	}
	if count, _, _ := wire.Varint(reward, 4); count != 1 {
		t.Fatalf("smelting mileage count=%d", count)
	}
	if currency := wallet.Snapshot(); currency.Gold != 920 || currency.EquipMileage != 1 || currency.EquipMileageExchangeGage != 20 {
		t.Fatalf("smelting wallet=%+v", currency)
	}
	if _, replay, _, err := store.Handle("/EquipSmelting", request); err != nil || string(replay) != string(response) {
		t.Fatalf("smelting replay differs err=%v", err)
	}
	if currency := wallet.Snapshot(); currency.Gold != 920 || currency.EquipMileage != 1 || currency.EquipMileageExchangeGage != 20 {
		t.Fatalf("smelting replay charged again: %+v", currency)
	}
}

func TestEquipmentSmeltingFailureConsumesAndReturnsCandidateGrades(t *testing.T) {
	dir := t.TempDir()
	wallet, _ := OpenWallet(testStore(filepath.Join(dir, "wallet.json")), Currency{Gold: 1000})
	inventory, _ := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	materials, _ := inventory.GrantOnce("refine-material", []gamedata.BattleReward{{Type: 8, ID: 10, Count: 30}})
	store, _ := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	if err := store.AttachSmelting(smeltingTestDesign([][]float64{{1, 0, 0, 0}, {1, 0, 0, 0}, {1, 0, 0, 0}}), wallet, inventory); err != nil {
		t.Fatal(err)
	}
	entry, _ := store.GrantOnce("refinable", 943035)
	store.owned.Equipment[0].Level = 9
	store.owned.Equipment[0].Rank = []uint64{4, 4, 4}
	if err := store.commitLocked(cloneEquipmentSnapshot(store.owned), "test setup"); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, entry.InvenIndex)
	request = wire.AppendBytes(request, 3, ItemWire(Item{Type: 4, Count: 80}))
	request = wire.AppendBytes(request, 3, ItemWire(materials[0]))
	_, response, _, err := store.Handle("/EquipSmelting", request)
	if err != nil {
		t.Fatal(err)
	}
	if result, _, _ := wire.Varint(response, 3); result != equipUpgradeFail {
		t.Fatalf("smelting failure result=%d", result)
	}
	var grades []uint64
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number == 4 {
			value, _ := binary.Uvarint(field.Value)
			grades = append(grades, value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(grades) != 3 || grades[0] != 1 || grades[1] != 1 || grades[2] != 1 {
		t.Fatalf("failure candidate grades=%v", grades)
	}
	if got := store.All()[0].Rank; got[0] != 4 || got[1] != 4 || got[2] != 4 {
		t.Fatalf("failure overwrote ranks=%v", got)
	}
	if currency := wallet.Snapshot(); currency.Gold != 920 || currency.EquipMileageExchangeGage != 30 {
		t.Fatalf("failure did not consume: %+v", currency)
	}
}

func TestEquipmentSequenceSmeltingRepeatsAndStopsAtTargetScore(t *testing.T) {
	dir := t.TempDir()
	wallet, _ := OpenWallet(testStore(filepath.Join(dir, "wallet.json")), Currency{Gold: 1000})
	inventory, _ := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	_, _ = inventory.GrantOnce("refine-material", []gamedata.BattleReward{{Type: 8, ID: 10, Count: 300}})
	store, _ := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	if err := store.AttachSmelting(smeltingTestDesign([][]float64{{1, 0, 0, 0}, {0, 0, 0, 1}, {0, 0, 0, 1}}), wallet, inventory); err != nil {
		t.Fatal(err)
	}
	entry, _ := store.GrantOnce("refinable", 943035)
	store.owned.Equipment[0].Level = 9
	store.owned.Equipment[0].Rank = []uint64{4, 1, 1}
	if err := store.commitLocked(cloneEquipmentSnapshot(store.owned), "test setup"); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, entry.InvenIndex)
	request = wire.AppendVarint(request, 3, 100)
	request = wire.AppendVarint(request, 4, 9)
	code, response, handled, err := store.Handle("/EquipSequenceSmelting", request)
	if err != nil || !handled || code != 177 {
		t.Fatalf("sequence smelting code=%d handled=%v err=%v", code, handled, err)
	}
	if result, _, _ := wire.Varint(response, 3); result != equipUpgradeStopTargetLevel {
		t.Fatalf("sequence smelting result=%d", result)
	}
	if attempts, _, _ := wire.Varint(response, 4); attempts != 1 {
		t.Fatalf("sequence smelting attempts=%d", attempts)
	}
	if successes, _, _ := wire.Varint(response, 9); successes != 1 {
		t.Fatalf("sequence smelting successes=%d", successes)
	}
	if currency := wallet.Snapshot(); currency.Gold != 920 || currency.EquipMileageExchangeGage != 30 {
		t.Fatalf("sequence smelting wallet=%+v", currency)
	}
}

func TestEquipmentSequenceSmeltingPacketLimitIsNotTerminal(t *testing.T) {
	dir := t.TempDir()
	wallet, _ := OpenWallet(testStore(filepath.Join(dir, "wallet.json")), Currency{Gold: 1000})
	inventory, _ := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	_, _ = inventory.GrantOnce("refine-material", []gamedata.BattleReward{{Type: 8, ID: 10, Count: 300}})
	store, _ := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	if err := store.AttachSmelting(smeltingTestDesign([][]float64{{1, 0, 0, 0}, {1, 0, 0, 0}, {1, 0, 0, 0}}), wallet, inventory); err != nil {
		t.Fatal(err)
	}
	entry, _ := store.GrantOnce("refinable", 943035)
	store.owned.Equipment[0].Level = 9
	store.owned.Equipment[0].Rank = []uint64{1, 1, 1}
	if err := store.commitLocked(cloneEquipmentSnapshot(store.owned), "test setup"); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 3), 2, entry.InvenIndex)
	request = wire.AppendVarint(request, 3, 2)
	request = wire.AppendVarint(request, 4, 12)
	_, response, _, err := store.Handle("/EquipSequenceSmelting", request)
	if err != nil {
		t.Fatal(err)
	}
	if result, _, _ := wire.Varint(response, 3); result != equipUpgradeSuccess {
		t.Fatalf("packet exhaustion result=%d want non-terminal success", result)
	}
	if attempts, _, _ := wire.Varint(response, 4); attempts != 2 {
		t.Fatalf("sequence smelting attempts=%d", attempts)
	}
}

func smeltingTestDesign(ratios [][]float64) *gamedata.EquipmentSmeltingDesign {
	design := &gamedata.EquipmentSmeltingDesign{
		Equipment: map[uint64]gamedata.EquipmentSmeltingItem{943035: {Grade: 4, RankGroup: 904, MaxLevel: 9}},
		Ranks:     map[[2]uint64]gamedata.EquipmentSmeltingRank{},
		Grades:    map[uint64][]gamedata.PromotionCost{4: {{Type: 4, Count: 80}, {Type: 8, ID: 10, Count: 30}}},
		Mileage:   gamedata.EquipmentSmeltingMileage{UseType: 8, UseID: 10, UseCount: 1000, RewardType: 68, RewardCount: 1},
		MaxStreak: 5000,
	}
	for i := 0; i < 3; i++ {
		design.Ranks[[2]uint64{904, uint64(i + 1)}] = gamedata.EquipmentSmeltingRank{Values: []uint64{1, 2, 3, 4}, GrowthPoint: []uint64{1, 2, 3, 4}, Ratio: ratios[i]}
	}
	return design
}

func TestEquipmentUpgradeFailureConsumesGoldWithoutLevel(t *testing.T) {
	dir := t.TempDir()
	wallet, err := OpenWallet(testStore(filepath.Join(dir, "wallet.json")), Currency{Gold: 200})
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	if err != nil {
		t.Fatal(err)
	}
	design := &gamedata.EquipmentUpgradeDesign{MaxLevel: map[uint64]uint64{1: 1}, Group: map[uint64]uint64{1: 2}, Levels: map[[2]uint64]gamedata.EquipmentUpgradeLevel{{2, 0}: {Costs: []gamedata.PromotionCost{{Type: 4, Count: 50}}, SuccessRatio: 0}}}
	items, err := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AttachUpgrade(design, wallet, items); err != nil {
		t.Fatal(err)
	}
	entry, err := store.GrantOnce("failure", 1)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, entry.InvenIndex)
	request = wire.AppendBytes(request, 3, ItemWire(Item{Type: 4, Count: 50}))
	_, response, _, err := store.Handle("/EquipUpgrade", request)
	if err != nil {
		t.Fatal(err)
	}
	if result, _, _ := wire.Varint(response, 2); result != equipUpgradeFail {
		t.Fatalf("failure result=%d", result)
	}
	if got := store.All()[0]; got.Level != 0 || got.UpgradeAttempts != 1 {
		t.Fatalf("failed upgrade state=%+v", got)
	}
	if wallet.Snapshot().Gold != 150 {
		t.Fatalf("failure gold=%d", wallet.Snapshot().Gold)
	}
}

func TestEquipmentUpgradeConsumesResourceStacksAndSequenceStopsWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	wallet, err := OpenWallet(testStore(filepath.Join(dir, "wallet.json")), Currency{Gold: 1})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	resources, err := inventory.GrantOnce("upgrade-items", []gamedata.BattleReward{{Type: 8, ID: 3001, Count: 2}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	if err != nil {
		t.Fatal(err)
	}
	design := &gamedata.EquipmentUpgradeDesign{
		MaxLevel: map[uint64]uint64{10: 3}, Group: map[uint64]uint64{10: 20},
		RankGroup: map[uint64]uint64{10: 30}, RankRatio: map[[2]uint64][]float64{{30, 1}: {1, 0, 0, 0}},
		Levels: map[[2]uint64]gamedata.EquipmentUpgradeLevel{
			{20, 0}: {Costs: []gamedata.PromotionCost{{Type: 8, ID: 3001, Count: 1}}, SuccessRatio: 1},
			{20, 1}: {Costs: []gamedata.PromotionCost{{Type: 8, ID: 3001, Count: 1}}, SuccessRatio: 1},
			{20, 2}: {Costs: []gamedata.PromotionCost{{Type: 8, ID: 3001, Count: 1}}, SuccessRatio: 1},
		},
	}
	if err := store.AttachUpgrade(design, wallet, inventory); err != nil {
		t.Fatal(err)
	}
	equipment, err := store.GrantOnce("resource-upgrade", 10)
	if err != nil {
		t.Fatal(err)
	}
	first := resources[0]
	first.Count = 1
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, equipment.InvenIndex)
	request = wire.AppendBytes(request, 3, ItemWire(first))
	if _, _, _, err := store.Handle("/EquipUpgrade", request); err != nil {
		t.Fatal(err)
	}
	sequence := wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, equipment.InvenIndex)
	sequence = wire.AppendVarint(sequence, 3, 10)
	sequence = wire.AppendVarint(sequence, 6, 3)
	_, response, _, err := store.Handle("/EquipSequenceUpgrade", sequence)
	if err != nil {
		t.Fatal(err)
	}
	if result, _, _ := wire.Varint(response, 3); result != equipUpgradeStopNotEnough {
		t.Fatalf("resource sequence result=%d", result)
	}
	var lack []byte
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number == 6 {
			lack = field.Value
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if itemType, _, _ := wire.Varint(lack, 3); itemType != 8 {
		t.Fatalf("missing resource type=%d", itemType)
	}
	if attempts, _, _ := wire.Varint(response, 4); attempts != 1 {
		t.Fatalf("resource sequence attempts=%d", attempts)
	}
	if got := store.All()[0]; got.Level != 2 {
		t.Fatalf("resource upgraded level=%d", got.Level)
	}
	if err := inventory.CanConsume([]Item{first}); err == nil {
		t.Fatal("resource stack was not exhausted")
	}
}

func TestEquipmentCustomMarkSetDeletePersistsAndReturnsInInfo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "equipment.json")
	store, err := OpenEquipmentInventory(testStore(path))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.GrantOnce("marked", 943035)
	if err != nil {
		t.Fatal(err)
	}
	set := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, entry.InvenIndex)
	set = wire.AppendBytes(set, 3, []byte("~15|5"))
	code, response, handled, err := store.Handle("/EquipMarkSet", set)
	if err != nil || !handled || code != 396 || len(response) != 0 {
		t.Fatalf("mark set code=%d handled=%v response=%x err=%v", code, handled, response, err)
	}
	restored, err := OpenEquipmentInventory(testStore(path))
	if err != nil {
		t.Fatal(err)
	}
	infoRequest := wire.AppendVarint(nil, 1, 2)
	_, info, _, err := restored.Handle("/EquipInfo", infoRequest)
	if err != nil {
		t.Fatal(err)
	}
	equipment, found, err := wire.Bytes(info, 1)
	if err != nil || !found {
		t.Fatalf("marked equipment missing: %v", err)
	}
	mark, found, err := wire.Bytes(equipment, 8)
	if err != nil || !found || string(mark) != "~15|5" {
		t.Fatalf("mark=%q found=%v err=%v", mark, found, err)
	}
	deleteRequest := wire.AppendVarint(wire.AppendVarint(nil, 1, 3), 2, entry.InvenIndex)
	code, response, handled, err = restored.Handle("/EquipMarkDelete", deleteRequest)
	if err != nil || !handled || code != 397 || len(response) != 0 {
		t.Fatalf("mark delete code=%d handled=%v response=%x err=%v", code, handled, response, err)
	}
	cleared, err := OpenEquipmentInventory(testStore(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := cleared.All(); len(got) != 1 || got[0].Mark != "" {
		t.Fatalf("cleared equipment=%+v", got)
	}
	for _, invalid := range []string{"", "~0|0", "~16|0", "~1|6", "abc|0", "x", "_|0"} {
		request := wire.AppendVarint(wire.AppendVarint(nil, 1, 4), 2, entry.InvenIndex)
		if invalid != "" {
			request = wire.AppendBytes(request, 3, []byte(invalid))
		}
		if _, _, _, err := cleared.Handle("/EquipMarkSet", request); err == nil {
			t.Fatalf("invalid mark %q accepted", invalid)
		}
	}
}

func TestEquipmentLockAndProtoDefaultUnlockPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "equipment.json")
	store, err := OpenEquipmentInventory(testStore(path))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.GrantOnce("lockable", 943035)
	if err != nil {
		t.Fatal(err)
	}
	lock := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, entry.InvenIndex), 3, 1)
	code, response, handled, err := store.Handle("/EquipLock", lock)
	if err != nil || !handled || code != 38 || len(response) != 0 {
		t.Fatalf("lock code=%d handled=%v response=%x err=%v", code, handled, response, err)
	}
	locked, err := OpenEquipmentInventory(testStore(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := locked.All(); len(got) != 1 || got[0].LockFlag != 1 {
		t.Fatalf("locked equipment=%+v", got)
	}
	unlock := wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, entry.InvenIndex)
	code, response, handled, err = locked.Handle("/EquipLock", unlock)
	if err != nil || !handled || code != 38 || len(response) != 0 {
		t.Fatalf("unlock code=%d handled=%v response=%x err=%v", code, handled, response, err)
	}
	unlocked, err := OpenEquipmentInventory(testStore(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := unlocked.All(); len(got) != 1 || got[0].LockFlag != 0 {
		t.Fatalf("unlocked equipment=%+v", got)
	}
	invalid := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 3), 2, entry.InvenIndex), 3, 2)
	if _, _, _, err := unlocked.Handle("/EquipLock", invalid); err == nil {
		t.Fatal("invalid lock flag accepted")
	}
}

func TestEquipmentGrantPersistsAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "equipment.json")
	store, err := OpenEquipmentInventory(testStore(path))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.GrantOnce("pack21:quest28:equip10010", 10010)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.GrantOnce("pack21:quest28:equip10010", 10010)
	if err != nil {
		t.Fatal(err)
	}
	if first.InvenIndex != again.InvenIndex || first.ID != again.ID || first.InvenIndex == 0 || first.ID != 10010 {
		t.Fatalf("grants: %+v %+v", first, again)
	}
	restored, err := OpenEquipmentInventory(testStore(path))
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	code, response, ok, err := restored.Handle("/EquipInfo", request)
	if err != nil || !ok || code != 34 {
		t.Fatalf("handle: %d %v %v", code, ok, err)
	}
	encoded, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("equipment missing: %v", err)
	}
	index, _, _ := wire.Varint(encoded, 1)
	base, _, _ := wire.Bytes(encoded, 5)
	id, _, _ := wire.Varint(base, 1)
	if index != first.InvenIndex || id != 10010 {
		t.Fatalf("wire index=%d id=%d", index, id)
	}
	var ranks []uint64
	if err := wire.Walk(base, func(field wire.Field) error {
		if field.Number == 6 {
			value, _ := binary.Uvarint(field.Value)
			ranks = append(ranks, value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(ranks) != 3 || ranks[0] != 0 || ranks[1] != 0 || ranks[2] != 0 {
		t.Fatalf("initial rank slots=%v, want three explicit zeroes", ranks)
	}
}

func TestEquipmentUsePersistsCharacterBinding(t *testing.T) {
	dir := t.TempDir()
	starter := &Starter{Version: "2.34.13"}
	items, err := OpenInventory(testStore(filepath.Join(dir, "items.json")), starter)
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(testStore(filepath.Join(dir, "characters.json")),
		[]Character{{InvenIndex: 535607162, ID: 350, Level: 20}}, items, "", "")
	if err != nil {
		t.Fatal(err)
	}
	equipment, err := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	if err != nil {
		t.Fatal(err)
	}
	if err := equipment.AttachCharacters(characters); err != nil {
		t.Fatal(err)
	}
	entry, err := equipment.GrantOnce("quest28", 10010)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, entry.InvenIndex)
	request = wire.AppendVarint(request, 3, 535607162)
	code, response, ok, err := equipment.Handle("/EquipUse", request)
	if err != nil || !ok || code != 35 {
		t.Fatalf("use: code=%d ok=%v err=%v", code, ok, err)
	}
	character, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("response character: %v", err)
	}
	index, _, _ := wire.Varint(character, 1)
	if index != 535607162 {
		t.Fatalf("character index=%d", index)
	}
	restored, err := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	if err != nil {
		t.Fatal(err)
	}
	infoRequest := wire.AppendVarint(nil, 1, 2)
	_, info, _, err := restored.Handle("/EquipInfo", infoRequest)
	if err != nil {
		t.Fatal(err)
	}
	encoded, found, err := wire.Bytes(info, 1)
	if err != nil || !found {
		t.Fatalf("restored equipment: %v", err)
	}
	useChar, found, err := wire.Varint(encoded, 2)
	if err != nil || !found || useChar != 535607162 {
		t.Fatalf("useChar=%d found=%v err=%v", useChar, found, err)
	}
}

func TestEquipmentClearPersistsUnboundEquipmentAndReturnsCharacter(t *testing.T) {
	dir := t.TempDir()
	items, err := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(testStore(filepath.Join(dir, "characters.json")),
		[]Character{{InvenIndex: 535607162, ID: 350, Level: 20}}, items, "", "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "equipment.json")
	equipment, err := OpenEquipmentInventory(testStore(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := equipment.AttachCharacters(characters); err != nil {
		t.Fatal(err)
	}
	entry, err := equipment.GrantOnce("equipped", 943035)
	if err != nil {
		t.Fatal(err)
	}
	equipment.owned.Equipment[0].UseChar = 535607162
	equipment.owned.Equipment[0].LockFlag = 1 // Lock prevents disposal, not unequipping.
	if err := equipment.commitLocked(cloneEquipmentSnapshot(equipment.owned), "test setup"); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 9), 2, entry.InvenIndex)
	request = wire.AppendVarint(request, 3, 535607162)
	code, response, handled, err := equipment.Handle("/EquipClear", request)
	if err != nil || !handled || code != 36 {
		t.Fatalf("clear code=%d handled=%v err=%v", code, handled, err)
	}
	character, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("missing clear character: %v", err)
	}
	if index, _, _ := wire.Varint(character, 1); index != 535607162 {
		t.Fatalf("clear character index=%d", index)
	}
	restored, err := OpenEquipmentInventory(testStore(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.All(); len(got) != 1 || got[0].UseChar != 0 || got[0].LockFlag != 1 {
		t.Fatalf("cleared equipment=%+v", got)
	}
	if _, _, _, err := equipment.Handle("/EquipClear", request); err == nil {
		t.Fatal("already-cleared equipment accepted")
	}
	wrongCharacter := wire.AppendVarint(wire.AppendVarint(nil, 1, 10), 2, entry.InvenIndex)
	wrongCharacter = wire.AppendVarint(wrongCharacter, 3, 999)
	if _, _, _, err := equipment.Handle("/EquipClear", wrongCharacter); err == nil {
		t.Fatal("unknown character accepted")
	}
}

func TestEquipmentChangeReplacesOnlyMatchingGameDataSlot(t *testing.T) {
	dir := t.TempDir()
	items, err := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(testStore(filepath.Join(dir, "characters.json")),
		[]Character{{InvenIndex: 535607162, ID: 350, Level: 20}}, items, "", "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "equipment.json")
	equipment, err := OpenEquipmentInventory(testStore(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := equipment.AttachCharacters(characters); err != nil {
		t.Fatal(err)
	}
	if err := equipment.AttachSlots(map[uint64]uint64{10010: 0, 943009: 0, 943619: 4}); err != nil {
		t.Fatal(err)
	}
	old, err := equipment.GrantOnce("old", 10010)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := equipment.GrantOnce("replacement", 943009)
	if err != nil {
		t.Fatal(err)
	}
	otherSlot, err := equipment.GrantOnce("other-slot", 943619)
	if err != nil {
		t.Fatal(err)
	}
	old.UseChar = 535607162
	otherSlot.UseChar = 535607162
	equipment.owned.Equipment[0] = old
	equipment.owned.Equipment[2] = otherSlot
	if err := equipment.commitLocked(cloneEquipmentSnapshot(equipment.owned), "test setup"); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 7), 2, replacement.InvenIndex)
	request = wire.AppendVarint(request, 3, 535607162)
	code, response, handled, err := equipment.Handle("/EquipChange", request)
	if err != nil || !handled || code != 45 {
		t.Fatalf("change code=%d handled=%v err=%v", code, handled, err)
	}
	character, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("missing response character: %v", err)
	}
	if index, _, _ := wire.Varint(character, 1); index != 535607162 {
		t.Fatalf("response character=%d", index)
	}
	restored, err := OpenEquipmentInventory(testStore(path))
	if err != nil {
		t.Fatal(err)
	}
	got := restored.All()
	if got[0].UseChar != 0 || got[1].UseChar != 535607162 || got[2].UseChar != 535607162 {
		t.Fatalf("changed equipment=%+v", got)
	}
}
