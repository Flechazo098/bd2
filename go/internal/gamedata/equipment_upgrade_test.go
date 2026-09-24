package gamedata

import (
	"database/sql"
	"encoding/binary"
	"math"
	"os"
	"testing"

	"bd2server/internal/wire"
)

func TestEquipmentUpgradeDesignReadsCostsAndRatio(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE EquipmentTable (id INTEGER PRIMARY KEY, ProtoBuf BLOB); CREATE TABLE EquipmentGrowthTable (groupId INTEGER,id INTEGER,ProtoBuf BLOB,PRIMARY KEY(groupId,id)); CREATE TABLE EquipmentRankTable (groupId INTEGER,id INTEGER,ProtoBuf BLOB,PRIMARY KEY(groupId,id))"); err != nil {
		t.Fatal(err)
	}
	equipment := testVarintField(testVarintField(testVarintField(nil, 4, 954), 13, 9), 19, 904)
	growth := testVarintField(testVarintField(nil, 4, 954), 5, 670)
	growth = testPackedField(growth, 1, 2)
	growth = testPackedField(growth, 2, 201)
	growth = testPackedField(growth, 3, 8)
	growth = testPackedField(growth, 7, 960)
	growth = testPackedField(growth, 8, 0)
	growth = testPackedField(growth, 9, 4)
	growth = wire.AppendDouble(growth, 10, 0.7)
	if _, err := db.Exec("INSERT INTO EquipmentTable VALUES (?,?)", 943035, equipment); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO EquipmentGrowthTable VALUES (?,?,?)", 954, 0, growth); err != nil {
		t.Fatal(err)
	}
	var ratio []byte
	for _, value := range []float32{1, 0, 0, 0} {
		var raw [4]byte
		binary.LittleEndian.PutUint32(raw[:], math.Float32bits(value))
		ratio = append(ratio, raw[:]...)
	}
	for slot := 1; slot <= 3; slot++ {
		if _, err := db.Exec("INSERT INTO EquipmentRankTable VALUES (?,?,?)", 904, slot, wire.AppendBytes(nil, 4, ratio)); err != nil {
			t.Fatal(err)
		}
	}
	design, err := loadEquipmentUpgradeDesign(db)
	if err != nil {
		t.Fatal(err)
	}
	level, maximum, err := design.Level(943035, 0)
	if err != nil || maximum != 9 || level.GrowthPoint != 670 || len(level.Costs) != 1 || level.Costs[0] != (PromotionCost{Type: 4, Count: 960}) || math.Abs(level.SuccessRatio-0.7) > 1e-12 {
		t.Fatalf("level=%+v maximum=%d err=%v", level, maximum, err)
	}
	if rank, err := design.RollRank(943035, 1); err != nil || rank != 1 {
		t.Fatalf("rank=%d err=%v", rank, err)
	}
	if rewards, err := design.BreakRewards(943035, 0); err != nil || len(rewards) != 1 || rewards[0] != (BattleReward{Type: 8, ID: 201, Count: 2}) {
		t.Fatalf("break rewards=%+v err=%v", rewards, err)
	}
}

func TestEquipmentUpgradeAgainstInstalledVersion23413(t *testing.T) {
	root := os.Getenv("BD2_TEST_GAMEDATA_ROOT")
	if root == "" {
		t.Skip("set BD2_TEST_GAMEDATA_ROOT for installed GameData integration test")
	}
	design, err := LoadEquipmentUpgradeDesign(root, "20260910162539")
	if err != nil {
		t.Fatal(err)
	}
	level, maximum, err := design.Level(943035, 0)
	if err != nil || maximum != 9 || len(level.Costs) != 1 || level.Costs[0] != (PromotionCost{Type: 4, Count: 960}) || level.SuccessRatio != 1 {
		t.Fatalf("installed level=%+v maximum=%d err=%v", level, maximum, err)
	}
	ratios := design.RankRatio[[2]uint64{904, 1}]
	if design.RankGroup[943035] != 904 || len(ratios) != 4 || math.Abs(ratios[0]-0.45) > 1e-6 || math.Abs(ratios[3]-0.01) > 1e-6 {
		t.Fatalf("installed rank group=%d ratios=%v", design.RankGroup[943035], ratios)
	}
}
