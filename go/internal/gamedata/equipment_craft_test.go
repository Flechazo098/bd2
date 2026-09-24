package gamedata

import (
	"os"
	"testing"
)

func TestEquipmentCraftAgainstInstalledCurrentVersion(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("set BD2_REAL_GAMEDATA for installed GameData integration test")
	}
	design, err := LoadEquipmentCraftDesign(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	recipe, ok := design.Recipe(1)
	if !ok || recipe.TalentLevel != 1 || recipe.ResultCount != 1 || len(recipe.Costs) != 2 ||
		recipe.Costs[0] != (PromotionCost{Type: 8, ID: 204, Count: 3}) ||
		recipe.Costs[1] != (PromotionCost{Type: 8, ID: 201, Count: 3}) {
		t.Fatalf("recipe=%+v found=%t", recipe, ok)
	}
	design.draw = func(limit uint64) (uint64, error) { return 0, nil }
	generated, err := design.Generate(1)
	if err != nil || generated.Design.ID != 10010 || len(generated.Main) == 0 {
		t.Fatalf("generated=%+v err=%v", generated, err)
	}
	gain, catalyst, maximum, err := design.Talent(140, 1, 1, 1, 0)
	if err != nil || gain == 0 || catalyst == 0 || maximum == 0 || gain > maximum {
		t.Fatalf("talent gain=%d catalyst=%d maximum=%d err=%v", gain, catalyst, maximum, err)
	}
}

func TestEquipmentUpgradeBreakAgainstInstalledCurrentVersion(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("set BD2_REAL_GAMEDATA for installed GameData integration test")
	}
	design, err := LoadEquipmentUpgradeDesign(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	rewards, err := design.BreakRewards(10010, 0)
	if err != nil || len(rewards) == 0 {
		t.Fatalf("break rewards=%+v err=%v", rewards, err)
	}
}
