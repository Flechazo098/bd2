package gamedata

import (
	"os"
	"testing"
)

func TestEquipmentSmeltingAgainstInstalledVersion23413(t *testing.T) {
	root := os.Getenv("BD2_TEST_GAMEDATA_ROOT")
	if root == "" {
		t.Skip("set BD2_TEST_GAMEDATA_ROOT for installed GameData integration test")
	}
	design, err := LoadEquipmentSmeltingDesign(root, "20260910162539")
	if err != nil {
		t.Fatal(err)
	}
	equipment := design.Equipment[943035]
	if equipment.Grade != 4 || equipment.RankGroup != 904 || equipment.MaxLevel != 9 {
		t.Fatalf("UR equipment smelting design=%+v", equipment)
	}
	cost, err := design.Cost(943035)
	if err != nil || len(cost) != 2 || cost[0] != (PromotionCost{Type: 4, Count: 80}) || cost[1] != (PromotionCost{Type: 8, ID: 10, Count: 30}) {
		t.Fatalf("UR smelting cost=%+v err=%v", cost, err)
	}
	if design.MaxStreak != 5000 || design.Mileage != (EquipmentSmeltingMileage{UseType: 8, UseID: 10, UseCount: 1000, RewardType: 68, RewardCount: 1}) {
		t.Fatalf("smelting limit=%d mileage=%+v", design.MaxStreak, design.Mileage)
	}
	if got := design.Ranks[[2]uint64{904, 1}].Values; len(got) != 4 || got[0] != 1 || got[3] != 4 {
		t.Fatalf("UR slot1 score values=%v", got)
	}
	if got := design.Ranks[[2]uint64{904, 1}].GrowthPoint; len(got) != 4 || got[0] != 34 || got[3] != 135 {
		t.Fatalf("UR slot1 growth points=%v", got)
	}
	if score, err := design.Score(943035, []uint64{1, 2, 3}); err != nil || score != 1+4+9 {
		t.Fatalf("score=%d err=%v", score, err)
	}
}

func TestEquipmentSmeltingMaximumRanksComeFromDesign(t *testing.T) {
	design := &EquipmentSmeltingDesign{
		Equipment: map[uint64]EquipmentSmeltingItem{1: {RankGroup: 9}},
		Ranks: map[[2]uint64]EquipmentSmeltingRank{
			{9, 1}: {Values: []uint64{1, 2}},
			{9, 2}: {Values: []uint64{1, 2, 3}},
			{9, 3}: {Values: []uint64{1, 2, 3, 4}},
		},
	}
	got, err := design.MaximumRanks(1)
	if err != nil || len(got) != 3 || got[0] != 2 || got[1] != 3 || got[2] != 4 {
		t.Fatalf("maximum ranks=%v err=%v", got, err)
	}
}

func TestEquipmentSmeltingRejectsInvalidRank(t *testing.T) {
	design := &EquipmentSmeltingDesign{
		Equipment: map[uint64]EquipmentSmeltingItem{1: {Grade: 4, RankGroup: 9, MaxLevel: 9}},
		Ranks: map[[2]uint64]EquipmentSmeltingRank{
			{9, 1}: {Values: []uint64{1, 2, 3, 4}},
			{9, 2}: {Values: []uint64{2, 4, 6, 8}},
			{9, 3}: {Values: []uint64{3, 6, 9, 12}},
		},
	}
	if _, err := design.Score(1, []uint64{1, 0, 1}); err == nil {
		t.Fatal("uninitialized smelting rank accepted")
	}
	if score, err := design.Score(1, []uint64{4, 4, 4}); err != nil || score != 24 {
		t.Fatalf("maximum score=%d err=%v", score, err)
	}
}
