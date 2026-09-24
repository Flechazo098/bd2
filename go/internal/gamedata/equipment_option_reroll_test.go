package gamedata

import (
	"database/sql"
	"math"
	"os"
	"testing"

	"bd2server/internal/wire"
)

func TestEquipmentOptionRerollDesignReadsCostsAndRollsUnlockedSlots(t *testing.T) {
	db := optionRerollTestDatabase(t)
	defer db.Close()

	insertOptionRerollEquipment(t, db, 77, 9, []uint64{100}, []uint64{200, 200}, []uint64{300})
	insertOptionRerollCost(t, db, 9, []uint64{0, 5}, []uint64{1000, 5}, []uint64{0, 17}, []uint64{4, 8})
	insertOptionChoice(t, db, 100, 1, 100)
	insertOptionChoice(t, db, 200, 7, 25)
	insertOptionChoice(t, db, 200, 8, 75)
	insertOptionChoice(t, db, 300, 10, 100)

	design, err := loadEquipmentOptionRerollDesign(db)
	if err != nil {
		t.Fatal(err)
	}
	item, ok := design.Lookup(77)
	if !ok || item.OptionRerollID != 9 || len(item.MainGroups) != 1 || len(item.SubGroups) != 2 || len(item.PrivateGroups) != 1 {
		t.Fatalf("equipment=%+v found=%v", item, ok)
	}
	costs, err := design.Cost(77, 2)
	if err != nil || len(costs) != 2 || costs[0] != (PromotionCost{Type: 4, Count: 1000}) || costs[1] != (PromotionCost{Type: 8, ID: 17, Count: 15}) {
		t.Fatalf("costs=%+v err=%v", costs, err)
	}

	draws := []uint64{0, 24, 25}
	design.draw = func(limit uint64) (uint64, error) {
		value := draws[0]
		draws = draws[1:]
		if value >= limit {
			t.Fatalf("test draw %d outside limit %d", value, limit)
		}
		return value, nil
	}
	roll, err := design.RollUnlocked(77, EquipmentOptionRerollLocks{
		Main: []bool{false}, Sub: []bool{false, true}, Private: []bool{false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if roll.Main[0] != (EquipmentOptionChoice{GroupID: 100, ID: 1}) ||
		roll.Sub[0] != (EquipmentOptionChoice{GroupID: 200, ID: 7}) || roll.Sub[1] != (EquipmentOptionChoice{}) ||
		roll.Private[0] != (EquipmentOptionChoice{GroupID: 300, ID: 10}) {
		t.Fatalf("roll=%+v", roll)
	}
	if len(draws) != 0 {
		t.Fatalf("unused draws=%v", draws)
	}
}

func TestEquipmentOptionRerollDesignRejectsMismatchedCostArrays(t *testing.T) {
	db := optionRerollTestDatabase(t)
	defer db.Close()
	insertOptionRerollEquipment(t, db, 77, 9, []uint64{100}, []uint64{200}, nil)
	// LockItemCount has one entry but the other parallel arrays have two.
	insertOptionRerollCost(t, db, 9, []uint64{0}, []uint64{1000, 5}, []uint64{0, 17}, []uint64{4, 8})
	insertOptionChoice(t, db, 100, 1, 100)
	insertOptionChoice(t, db, 200, 7, 100)
	if _, err := loadEquipmentOptionRerollDesign(db); err == nil {
		t.Fatal("mismatched option-reroll resource arrays accepted")
	}
}

func TestEquipmentOptionRerollCostRejectsOverflowAndBadLocks(t *testing.T) {
	design := &EquipmentOptionRerollDesign{
		Equipment: map[uint64]EquipmentOptionRerollItem{1: {
			ID: 1, OptionRerollID: 2, MainGroups: []uint64{10}, SubGroups: []uint64{20},
		}},
		Costs: map[uint64]EquipmentOptionRerollCost{2: {Resources: []EquipmentOptionRerollResource{{
			Type: 8, ID: 17, BaseCount: math.MaxUint64, LockCount: 1,
		}}}},
		Groups: map[uint64]OptionGroup{
			10: {ID: 10, Choices: []WeightedOption{{ID: 1, Weight: 1}}},
			20: {ID: 20, Choices: []WeightedOption{{ID: 2, Weight: 1}}},
		},
	}
	if _, err := design.Cost(1, 1); err == nil {
		t.Fatal("overflowing lock cost accepted")
	}
	if _, err := design.RollUnlocked(1, EquipmentOptionRerollLocks{Main: []bool{false}}); err == nil {
		t.Fatal("short option lock arrays accepted")
	}
}

func TestEquipmentOptionRerollAgainstInstalledCurrentVersion(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		root = os.Getenv("BD2_TEST_GAMEDATA_ROOT")
	}
	if root == "" {
		t.Skip("set BD2_REAL_GAMEDATA for installed GameData integration test")
	}
	design, err := LoadEquipmentOptionRerollDesign(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	item, ok := design.Lookup(943035)
	if !ok || item.OptionRerollID != 9435 || item.PrivateUniqueCharID != 35 || len(item.MainGroups) != 2 || item.MainGroups[0] != 1943010 || item.MainGroups[1] != 2943010 ||
		len(item.SubGroups) != 3 || item.SubGroups[0] != 943000 || item.SubGroups[1] != 943000 || item.SubGroups[2] != 943000 ||
		len(item.PrivateGroups) != 1 || item.PrivateGroups[0] != 3943035 {
		t.Fatalf("943035 design=%+v found=%v", item, ok)
	}
	for locked, wantMaterial := range []uint64{60, 120, 180} {
		cost, err := design.Cost(943035, uint64(locked))
		if err != nil || len(cost) != 2 || cost[0] != (PromotionCost{Type: 4, Count: 120000}) || cost[1] != (PromotionCost{Type: 8, ID: 17, Count: wantMaterial}) {
			t.Fatalf("locked=%d cost=%+v err=%v", locked, cost, err)
		}
	}
	if choices := design.Groups[943000].Choices; len(choices) != 8 || choices[0].Weight != 100 {
		t.Fatalf("943000 choices=%+v", choices)
	}
	if choices := design.Groups[1943010].Choices; len(choices) != 2 || choices[0].ID != 3 || choices[1].ID != 4 {
		t.Fatalf("1943010 changeable main choices=%+v", choices)
	}
	if conversion := design.Conversion; conversion == nil || conversion.Ratio != 10 || conversion.SourceType != 8 || conversion.SourceID != 16 || conversion.TargetType != 8 || conversion.TargetID != 17 {
		t.Fatalf("reroll material conversion=%+v", conversion)
	}
}

func optionRerollTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE EquipmentTable (id INTEGER PRIMARY KEY, ProtoBuf BLOB);
		CREATE TABLE EquipmentOptionRerollTable (id INTEGER PRIMARY KEY, ProtoBuf BLOB);
		CREATE TABLE EquipmentRerollDefaultTable (id INTEGER PRIMARY KEY, ProtoBuf BLOB);
		CREATE TABLE EquipmentOptionTable (groupId INTEGER,id INTEGER,ProtoBuf BLOB,PRIMARY KEY(groupId,id));
	`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func insertOptionRerollEquipment(t *testing.T, db *sql.DB, id, rerollID uint64, main, sub, private []uint64) {
	t.Helper()
	proto := testVarintField(nil, 6, id)
	proto = testPackedFields(proto, 12, main)
	proto = testVarintField(proto, 15, rerollID)
	proto = testPackedFields(proto, 17, private)
	proto = testPackedFields(proto, 21, sub)
	if _, err := db.Exec("INSERT INTO EquipmentTable VALUES (?,?)", id, proto); err != nil {
		t.Fatal(err)
	}
}

func insertOptionRerollCost(t *testing.T, db *sql.DB, id uint64, locks, counts, ids, types []uint64) {
	t.Helper()
	proto := testVarintField(nil, 1, id)
	proto = testPackedFields(proto, 2, locks)
	proto = testPackedFields(proto, 3, counts)
	proto = testPackedFields(proto, 4, ids)
	proto = testPackedFields(proto, 5, types)
	if _, err := db.Exec("INSERT INTO EquipmentOptionRerollTable VALUES (?,?)", id, proto); err != nil {
		t.Fatal(err)
	}
}

func insertOptionChoice(t *testing.T, db *sql.DB, groupID, id, weight uint64) {
	t.Helper()
	proto := wire.AppendDouble(nil, 1, float64(id))
	proto = testVarintField(proto, 2, weight)
	proto = testVarintField(proto, 3, groupID)
	proto = testVarintField(proto, 5, id)
	if _, err := db.Exec("INSERT INTO EquipmentOptionTable VALUES (?,?,?)", groupID, id, proto); err != nil {
		t.Fatal(err)
	}
}

func testPackedFields(proto []byte, number int, values []uint64) []byte {
	for _, value := range values {
		proto = testPackedField(proto, number, value)
	}
	return proto
}
