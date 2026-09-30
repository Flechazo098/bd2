package gamedata

import (
	"os"
	"testing"
)

func TestInstalledPack21FirstMonsterRewards(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	rewards, err := BattleRewards(root, "20260910162539", 21, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rewards) != 1 || rewards[0] != (BattleReward{Type: 8, ID: 8, Count: 3}) {
		t.Fatalf("pack21 monster1 rewards = %+v, want slime type8/item8 x3", rewards)
	}
}

func TestInstalledPack22DeckAbsentFromPack21Rewards(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	rewards, err := BattleDeckRewards(root, "20260910162539", 22, 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(rewards) != 1 || rewards[0] != (BattleReward{Type: 8, ID: 14, Count: 1}) {
		t.Fatalf("pack22 deck9 rewards = %+v, want type8/item14 x1", rewards)
	}
	if _, err := BattleDeckRewards(root, "20260910162539", 21, 9); err == nil {
		t.Fatal("pack21 unexpectedly contains pack22-only deck9")
	}
}

func TestInstalledTutorialGrowthReachesLevel20(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	level, exp, refunds, err := CharacterGrowth(root, "20260910162539", 350, 1, 0, []GrowthMaterial{{ID: 8, Count: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if level != 20 || exp != 0 {
		t.Fatalf("char350 + resource8x3 = level %d exp %d, want level20 exp0", level, exp)
	}
	if len(refunds) != 2 || refunds[0] != (GrowthMaterial{ID: 8, Count: 1}) || refunds[1] != (GrowthMaterial{ID: 7, Count: 3}) {
		t.Fatalf("refunds=%v, want resource8x1 + resource7x3", refunds)
	}
}

func TestInstalledTutorialGrowthClientSelectionRefund(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	level, exp, refunds, err := CharacterGrowth(root, "20260910162539", 350, 1, 0, []GrowthMaterial{{ID: 8, Count: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if level != 20 || exp != 0 || len(refunds) != 1 || refunds[0] != (GrowthMaterial{ID: 7, Count: 3}) {
		t.Fatalf("growth = level%d exp%d refunds%v, want official resource7x3", level, exp, refunds)
	}
}
