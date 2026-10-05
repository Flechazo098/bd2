package gamedata

import (
	"os"
	"testing"
)

func TestEventPlayInstalledCatalog(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not set")
	}
	c, e := LoadEventPlayCatalog(root, "20260923193640")
	if e != nil {
		t.Fatal(e)
	}
	for _, n := range []string{"PackEventStoryTable", "FieldMiniGameSkillTable", "MGDRewardTable"} {
		if len(c.Tables[n]) == 0 {
			t.Fatalf("empty %s", n)
		}
	}
}
