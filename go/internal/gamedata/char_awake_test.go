package gamedata

import (
	"os"
	"testing"
)

func testCharAwakeDesign() *CharAwakeDesign {
	levels := func(stat uint64, values ...float64) []CharAwakeGrowth {
		result := make([]CharAwakeGrowth, len(values))
		for i, value := range values {
			result[i] = CharAwakeGrowth{ID: uint64(i + 1), Costs: []CharAwakeCost{{Type: 8, ID: 701 + uint64(i), Count: uint64(i + 1)}, {Type: 4, Count: 100}}, StatType: stat, StatValue: value}
		}
		return result
	}
	character := CharAwakeCharacter{UniqueCharID: 35, Active: true}
	character.ImprintGrowth[0] = levels(1, 5, 10)
	character.ImprintGrowth[1] = levels(2, .01, .02)
	character.ImprintGrowth[2] = levels(3, 2, 4)
	character.AwakeGrowth = []CharAwakeGrowth{
		{ID: 100, Costs: []CharAwakeCost{{Type: 8, ID: 705, Count: 20}, {Type: 4, Count: 500}}, StatType: 4, StatValue: .12},
		{ID: 101, StatType: 14, StatValue: .1},
	}
	return &CharAwakeDesign{
		Characters: map[uint64]CharAwakeCharacter{35: character},
		Stages:     map[uint64]CharAwakeCharacterStage{354: {UniqueCharID: 35, Grade: 5, GrowthGrade: 5, MaximumLevel: 100}},
	}
}

func TestCharAwakeImprintUsesCrossLevelCostsAndCumulativeStats(t *testing.T) {
	design := testCharAwakeDesign()
	costs, levels, err := design.ImprintCosts(35, [3]uint64{0, 0, 0}, []CharImprintTarget{{Slot: 1, TargetLevel: 2}, {Slot: 2, TargetLevel: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if levels != [3]uint64{2, 1, 0} || len(costs) != 3 {
		t.Fatalf("levels=%v costs=%+v", levels, costs)
	}
	var gold uint64
	for _, cost := range costs {
		if cost.Type == 4 {
			gold += cost.Count
		}
	}
	if gold != 300 {
		t.Fatalf("gold=%d want=300", gold)
	}
	stats, err := design.CharAwakeContributions(35, levels, false)
	if err != nil || len(stats) != 2 || stats[0].Flat != 10 || stats[1].Percent != .01 {
		t.Fatalf("cumulative target-level stats=%+v err=%v", stats, err)
	}
	if _, _, err := design.ImprintCosts(35, levels, []CharImprintTarget{{Slot: 1, TargetLevel: 1}}); err == nil {
		t.Fatal("imprint downgrade accepted")
	}
}

func TestCharAwakeActivationRequiresAllSlotsAndAddsEveryEffect(t *testing.T) {
	design := testCharAwakeDesign()
	if _, err := design.AwakeCosts(35, [3]uint64{2, 2, 1}, false); err == nil {
		t.Fatal("incomplete imprint slots accepted")
	}
	costs, err := design.AwakeCosts(35, [3]uint64{2, 2, 2}, false)
	if err != nil || len(costs) != 2 || costs[0].ID != 705 || costs[1].Type != 4 {
		t.Fatalf("awakening costs=%+v err=%v", costs, err)
	}
	stats, err := design.CharAwakeContributions(35, [3]uint64{2, 2, 2}, true)
	if err != nil || len(stats) != 5 || stats[3].Stat != StatAttack || stats[3].Percent != .12 || stats[4].Option != 14 {
		t.Fatalf("awakening stats=%+v err=%v", stats, err)
	}
}

func TestCharAwakeAgainstInstalledVersion23413(t *testing.T) {
	root := os.Getenv("BD2_TEST_GAMEDATA_ROOT")
	if root == "" {
		t.Skip("set BD2_TEST_GAMEDATA_ROOT for installed GameData integration test")
	}
	design, err := LoadCharAwakeDesign(root, "20260910162539")
	if err != nil {
		t.Fatal(err)
	}
	entry := design.Characters[35]
	if len(design.Characters) != 86 || len(entry.ImprintGrowth[0]) != 10 || len(entry.ImprintGrowth[1]) != 10 || len(entry.ImprintGrowth[2]) != 10 || len(entry.AwakeGrowth) != 2 {
		t.Fatalf("installed awakening design characters=%d char35=%+v", len(design.Characters), entry)
	}
	if entry.AwakeGrowth[0].Costs[0] != (CharAwakeCost{Type: 8, ID: 705, Count: 200}) || entry.AwakeGrowth[1].StatType != 14 || entry.AwakeGrowth[1].StatValue != .1 {
		t.Fatalf("installed char35 awakening=%+v", entry.AwakeGrowth)
	}
	if unique, err := design.ValidateGrowthCompleted(354, 100); err != nil || unique != 35 {
		t.Fatalf("installed final stage unique=%d err=%v", unique, err)
	}
}
