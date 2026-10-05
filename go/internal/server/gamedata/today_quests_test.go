package gamedata

import (
	"os"
	"testing"
)

func TestInstalledTodayQuestChains(t *testing.T) {
	root := "../../../../data/resources/GameData"
	if _, err := os.Stat(root); err != nil {
		t.Skip("installed GameData absent")
	}
	c, err := LoadTodayQuests(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Quests) != 397 || c.Limit != 3 || c.PostCount != 3 || c.AchievementScore != 7 {
		t.Fatalf("unexpected current defaults: %+v", c)
	}
	roots, finals := 0, 0
	for _, q := range c.Quests {
		if q.PriorID == 0 {
			roots++
		}
		if q.NextID == 0 {
			finals++
			if len(q.Rewards) != 1 || q.Rewards[0].Type != 4 || q.ReputationCompleteID != 1 {
				t.Fatalf("missing commissioned reward %d", q.ID)
			}
		}
	}
	if roots != 121 || finals != 121 {
		t.Fatalf("roots=%d finals=%d", roots, finals)
	}
}
