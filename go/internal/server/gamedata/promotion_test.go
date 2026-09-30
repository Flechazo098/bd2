package gamedata

import (
	"database/sql"
	"encoding/binary"
	"os"
	"testing"
)

func TestCharacterGrowthPromotionsAgainstInstalledVersion23413(t *testing.T) {
	root := os.Getenv("BD2_TEST_GAMEDATA_ROOT")
	if root == "" {
		t.Skip("set BD2_TEST_GAMEDATA_ROOT for installed GameData integration test")
	}
	submitted := []PromotionCost{
		{Type: 8, ID: 9, Count: 753},
		{Type: 8, ID: 11, Count: 1},
		{Type: 8, ID: 12, Count: 2},
		{Type: 8, ID: 13, Count: 3},
		{Type: 8, ID: 14, Count: 4},
		{Type: 4, Count: 10000},
	}
	result, err := CharacterGrowthPromotions(root, "20260910162539", 6510, 1, 0, submitted)
	if err != nil {
		t.Fatal(err)
	}
	if result.CharacterID != 6514 || result.Level != 100 || result.Exp != 0 || len(result.Costs) != 8 {
		t.Fatalf("installed 2.34.13 combined promotion=%+v", result)
	}
}

func testVarintField(dst []byte, field int, value uint64) []byte {
	dst = binary.AppendUvarint(dst, uint64(field<<3))
	return binary.AppendUvarint(dst, value)
}

func testPackedField(dst []byte, field int, values ...uint64) []byte {
	var packed []byte
	for _, value := range values {
		packed = binary.AppendUvarint(packed, value)
	}
	dst = binary.AppendUvarint(dst, uint64(field<<3|2))
	dst = binary.AppendUvarint(dst, uint64(len(packed)))
	return append(dst, packed...)
}

func TestCharacterPromotionUsesCharAndGrowthTables(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, schema := range []string{
		"CREATE TABLE CharTable (id INTEGER PRIMARY KEY, ProtoBuf BLOB)",
		"CREATE TABLE CharGrowthTable (id INTEGER PRIMARY KEY, ProtoBuf BLOB)",
		"CREATE TABLE CharLevelTable (GroupId INTEGER, id INTEGER, ProtoBuf BLOB, PRIMARY KEY(GroupId,id))",
	} {
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	current := testVarintField(nil, 1, 101)
	current = testVarintField(current, 15, 351)
	next := testVarintField(nil, 1, 102)
	growth := testVarintField(nil, 1, 101)
	growth = testPackedField(growth, 2, 1, 1000)
	growth = testPackedField(growth, 3, 11, 0)
	growth = testPackedField(growth, 4, 8, 4)
	growth = testVarintField(growth, 9, 20)
	if _, err := db.Exec("INSERT INTO CharTable VALUES (?,?),(?,?)", 350, current, 351, next); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO CharGrowthTable VALUES (?,?)", 101, growth); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO CharLevelTable VALUES (?,?,?)", 101, 20, []byte{}); err != nil {
		t.Fatal(err)
	}
	nextID, costs, err := characterPromotion(db, 350, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if nextID != 351 || len(costs) != 2 || costs[0] != (PromotionCost{Type: 8, ID: 11, Count: 1}) || costs[1] != (PromotionCost{Type: 4, Count: 1000}) {
		t.Fatalf("promotion next=%d costs=%+v", nextID, costs)
	}
	if _, _, err := characterPromotion(db, 350, 19, 0); err == nil {
		t.Fatal("promotion before the stage cap was accepted")
	}
}

func TestCharacterGrowthPromotionsCrossesTwoStagesWithCumulativeCosts(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, schema := range []string{
		"CREATE TABLE CharTable (id INTEGER PRIMARY KEY, ProtoBuf BLOB)",
		"CREATE TABLE CharGrowthTable (id INTEGER PRIMARY KEY, ProtoBuf BLOB)",
		"CREATE TABLE CharLevelTable (GroupId INTEGER, id INTEGER, ProtoBuf BLOB, PRIMARY KEY(GroupId,id))",
		"CREATE TABLE ResourceTable (id INTEGER PRIMARY KEY, ProtoBuf BLOB)",
	} {
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		id, growth, next uint64
	}{{352, 103, 353}, {353, 104, 354}, {354, 105, 0}} {
		proto := testVarintField(nil, 1, row.growth)
		if row.next != 0 {
			proto = testVarintField(proto, 15, row.next)
		}
		if _, err := db.Exec("INSERT INTO CharTable VALUES (?,?)", row.id, proto); err != nil {
			t.Fatal(err)
		}
	}
	growth103 := testVarintField(nil, 1, 103)
	growth103 = testPackedField(growth103, 2, 3, 3000)
	growth103 = testPackedField(growth103, 3, 13, 0)
	growth103 = testPackedField(growth103, 4, 8, 4)
	growth103 = testVarintField(growth103, 9, 60)
	growth104 := testVarintField(nil, 1, 104)
	growth104 = testPackedField(growth104, 2, 4, 4000)
	growth104 = testPackedField(growth104, 3, 14, 0)
	growth104 = testPackedField(growth104, 4, 8, 4)
	growth104 = testVarintField(growth104, 9, 80)
	growth105 := testVarintField(testVarintField(nil, 1, 105), 9, 100)
	for _, row := range []struct {
		id    uint64
		proto []byte
	}{{103, growth103}, {104, growth104}, {105, growth105}} {
		if _, err := db.Exec("INSERT INTO CharGrowthTable VALUES (?,?)", row.id, row.proto); err != nil {
			t.Fatal(err)
		}
	}
	for _, curve := range []struct{ group, first, maximum uint64 }{{103, 1, 60}, {104, 60, 80}, {105, 80, 100}} {
		for level := curve.first; level < curve.maximum; level++ {
			proto := testVarintField(nil, 8, 10)
			if _, err := db.Exec("INSERT INTO CharLevelTable VALUES (?,?,?)", curve.group, level, proto); err != nil {
				t.Fatal(err)
			}
		}
	}
	resource := testVarintField(nil, 9, 10)
	if _, err := db.Exec("INSERT INTO ResourceTable VALUES (?,?)", 9, resource); err != nil {
		t.Fatal(err)
	}
	submitted := []PromotionCost{
		{Type: 8, ID: 9, Count: 40},
		{Type: 8, ID: 13, Count: 3},
		{Type: 8, ID: 14, Count: 4},
		{Type: 4, Count: 7000},
	}
	result, err := characterGrowthPromotions(db, 352, 60, 0, submitted)
	if err != nil {
		t.Fatal(err)
	}
	if result.CharacterID != 354 || result.Level != 100 || result.Exp != 0 || len(result.Refunds) != 0 {
		t.Fatalf("combined result=%+v", result)
	}
	if len(result.Costs) != 4 {
		t.Fatalf("cumulative costs=%+v", result.Costs)
	}
	fromLevelOne := append([]PromotionCost(nil), submitted...)
	fromLevelOne[0].Count = 99
	result, err = characterGrowthPromotions(db, 352, 1, 0, fromLevelOne)
	if err != nil {
		t.Fatal(err)
	}
	if result.CharacterID != 354 || result.Level != 100 || result.Exp != 0 {
		t.Fatalf("level-one combined result=%+v", result)
	}
	bad := append([]PromotionCost(nil), submitted...)
	bad[len(bad)-1].Count = 6000
	if _, err := characterGrowthPromotions(db, 352, 60, 0, bad); err == nil {
		t.Fatal("non-prefix aggregate gold was accepted")
	}
}
