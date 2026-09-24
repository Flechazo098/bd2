package gamedata

import (
	"database/sql"
	"os"
	"testing"

	"bd2server/internal/wire"
)

func TestTalentGrowthDesignJoinsCharacterAndUsesCumulativeExperience(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE TalentTable (id INTEGER PRIMARY KEY, ProtoBuf BLOB); CREATE TABLE CharTable (id INTEGER PRIMARY KEY, ProtoBuf BLOB); CREATE TABLE TalentGrowthTable (groupId INTEGER,id INTEGER,ProtoBuf BLOB,PRIMARY KEY(groupId,id))"); err != nil {
		t.Fatal(err)
	}
	talent := testVarintField(testVarintField(nil, 6, 904), 11, 5)
	character := testVarintField(nil, 18, 904)
	if _, err := db.Exec("INSERT INTO TalentTable VALUES (?,?)", 904, talent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO CharTable VALUES (?,?)", 140, character); err != nil {
		t.Fatal(err)
	}
	needs := []uint64{14, 28, 404, 1008}
	gold := []uint64{1000, 2000, 4000, 10000}
	for level, need := range needs {
		growth := testPackedField(nil, 2, 1, gold[level])
		growth = testPackedField(growth, 3, uint64(level+3), 0)
		growth = testPackedField(growth, 4, 8, 4)
		growth = testVarintField(growth, 6, need)
		if _, err := db.Exec("INSERT INTO TalentGrowthTable VALUES (?,?,?)", 904, level+1, growth); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO TalentGrowthTable VALUES (?,?,?)", 904, 5, testVarintField(nil, 5, 5)); err != nil {
		t.Fatal(err)
	}
	design, err := loadTalentGrowthDesign(db)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := design.UpgradeRule(140, 3)
	if err != nil {
		t.Fatal(err)
	}
	if rule.TalentID != 904 || rule.GrowthGroup != 904 || rule.MaxLevel != 5 || rule.RequiredTotalExp != 446 {
		t.Fatalf("rule=%+v", rule)
	}
	want := []PromotionCost{{Type: 8, ID: 5, Count: 1}, {Type: 4, Count: 4000}}
	if len(rule.Costs) != len(want) || rule.Costs[0] != want[0] || rule.Costs[1] != want[1] {
		t.Fatalf("costs=%+v want=%+v", rule.Costs, want)
	}
	if _, err := design.UpgradeRule(140, 5); err == nil {
		t.Fatal("max-level talent was upgradeable")
	}
}

func TestTalentGrowthDesignRejectsMismatchedCostArrays(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE TalentTable (id INTEGER PRIMARY KEY, ProtoBuf BLOB); CREATE TABLE CharTable (id INTEGER PRIMARY KEY, ProtoBuf BLOB); CREATE TABLE TalentGrowthTable (groupId INTEGER,id INTEGER,ProtoBuf BLOB,PRIMARY KEY(groupId,id))"); err != nil {
		t.Fatal(err)
	}
	talent := testVarintField(testVarintField(nil, 6, 9), 11, 2)
	character := testVarintField(nil, 18, 9)
	broken := testPackedField(testPackedField(nil, 2, 1, 1000), 3, 3)
	broken = testPackedField(broken, 4, 8, 4)
	broken = wire.AppendVarint(broken, 6, 10)
	if _, err := db.Exec("INSERT INTO TalentTable VALUES (?,?)", 9, talent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO CharTable VALUES (?,?)", 1, character); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO TalentGrowthTable VALUES (?,?,?)", 9, 1, broken); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTalentGrowthDesign(db); err == nil {
		t.Fatal("mismatched talent costs were accepted")
	}
}

func TestTalentGrowthAgainstInstalledCurrentVersion(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("set BD2_REAL_GAMEDATA for installed GameData integration test")
	}
	design, err := LoadTalentGrowthDesign(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	rule, err := design.UpgradeRule(140, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []PromotionCost{{Type: 8, ID: 3, Count: 1}, {Type: 4, Count: 1000}}
	if rule.TalentID != 904 || rule.MaxLevel != 5 || rule.RequiredTotalExp != 14 || len(rule.Costs) != 2 || rule.Costs[0] != want[0] || rule.Costs[1] != want[1] {
		t.Fatalf("installed rule=%+v", rule)
	}
	if _, err := design.UpgradeRule(10140, 1); err == nil {
		t.Fatal("the current max-level-one talent was upgradeable")
	}
}
