package gamedata

import (
	"os"
	"testing"
)

func TestAggregateStatsAddsFlatHealth(t *testing.T) {
	got := AggregateStats(BaseStats{Health: 505}, []StatContribution{{Stat: StatHealth, Flat: 7}})
	if got.Health != 512 {
		t.Fatalf("health=%v, want 512", got.Health)
	}
}

func TestRealTutorialStatsDoNotMisclassifyAttackAsHealth(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	base, err := CharacterBaseStats(root, "20260910162539", 350, 20)
	if err != nil {
		t.Fatal(err)
	}
	if base.Health != 505 {
		t.Fatalf("base health=%v, want 505", base.Health)
	}
	option, err := EquipmentOptionContribution(root, "20260910162539", EquipmentOption{GroupID: 1010010, ID: 3})
	if err != nil {
		t.Fatal(err)
	}
	if option.Stat != StatAttack || option.Flat != 7 {
		t.Fatalf("option=%+v", option)
	}
	if got := AggregateStats(base, []StatContribution{option}); got.Health != 505 {
		t.Fatalf("attack equipment changed health: %+v", got)
	}
}

func TestLoadedCharacterStatDesignMatchesDirectLookup(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	design, err := LoadPictorialDesign(root, "20260910162539")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := design.CharStats.BaseStats(350, 20)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := CharacterBaseStats(root, "20260910162539", 350, 20)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != direct {
		t.Fatalf("loaded stats=%+v, direct database lookup=%+v", loaded, direct)
	}
	if _, err := design.CharStats.BaseStats(0, 20); err == nil {
		t.Fatal("zero character id unexpectedly resolved")
	}
	if _, err := design.CharStats.BaseStats(350, 0); err == nil {
		t.Fatal("zero character level unexpectedly resolved")
	}
}

func TestAggregateStatsAppliesPercentAfterFlat(t *testing.T) {
	got := AggregateStats(BaseStats{Health: 100}, []StatContribution{{Stat: StatHealth, Flat: 7, Percent: 0.1}})
	if got.Health != 117 {
		t.Fatalf("health=%v, want 117", got.Health)
	}
}

func TestAggregateStatsMatchesOfficialGrowthSnapshot(t *testing.T) {
	// Before tutorial equipment 10010 entered the pictorial book, the real
	// AllCharRefresh response advertised HEALTH_PERCENT=0.015. The subsequent
	// official CharGrowth response returned HP=512 for level-20 char 350.
	got := AggregateStats(BaseStats{Health: 505}, []StatContribution{{Stat: StatHealth, Percent: 0.015}})
	if got.Health != 512 {
		t.Fatalf("health=%v, want official snapshot 512", got.Health)
	}

	// Acquiring the tutorial equipment raised that account-wide pictorial
	// bonus to 0.0175. It displays as +8 HP, independently of the equipment's
	// own option 3, which is ATTACK_VALUE +7.
	afterPictorial := AggregateStats(BaseStats{Health: 505}, []StatContribution{{Stat: StatHealth, Percent: 0.0175}})
	if afterPictorial.Health != 513 {
		t.Fatalf("post-pictorial health=%v, want 513", afterPictorial.Health)
	}
}
