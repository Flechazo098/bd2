package gamedata

import (
	"os"
	"testing"
)

func TestRandomBoxOpenAggregatesOnlyKnownDeterministicReward(t *testing.T) {
	design := &RandomBoxDesign{rewards: map[uint64][]BattleReward{
		7: {{Type: 8, ID: 704, Count: 1}},
	}}
	got, err := design.Open(7, 100000)
	if err != nil || len(got) != 1 || got[0] != (BattleReward{Type: 8, ID: 704, Count: 100000}) {
		t.Fatalf("Open deterministic box = %+v, %v", got, err)
	}
	if _, err := design.Open(8, 1); err == nil {
		t.Fatal("accepted an unknown or weighted box")
	}
}

// This locks the actual 2.34.13 row behind the installed GameData opt-in,
// rather than replacing GameData lookup with a hand-written item mapping.
func TestLoadRandomBoxDesignInstalledEngravingEssence(t *testing.T) {
	root := os.Getenv("BD2_TEST_GAMEDATA_ROOT")
	if root == "" {
		t.Skip("set BD2_TEST_GAMEDATA_ROOT for installed GameData integration test")
	}
	design, err := LoadRandomBoxDesign(root, "20260910162539")
	if err != nil {
		t.Fatal(err)
	}
	got, err := design.Open(433302, 100000) // Essence of Perseverance box.
	if err != nil || len(got) != 1 || got[0] != (BattleReward{Type: 8, ID: 704, Count: 100000}) {
		t.Fatalf("433302 deterministic contents = %+v, %v", got, err)
	}
}
