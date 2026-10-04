package gamedata

import (
	"os"
	"testing"
)

func TestMonsterHuntHPStageBoundariesAndSignificantDigits(t *testing.T) {
	d := &MonsterHunt{MaxLevel: 30, baseHP: 6000, healthRate: 400, healthSlope: 2.6, stage2Level: 12, stage2Ratio: 1.02, stage3Level: 14, stage3Ratio: .86}
	// Independently evaluated client formula at the two strict greater-than
	// stage boundaries; truncation keeps three significant decimal digits.
	for level, want := range map[uint64]uint64{1: 6000, 2: 151000, 12: 168000000, 13: 231000000, 14: 303000000, 15: 330000000} {
		got, err := d.HP(level)
		if err != nil || got != want {
			t.Fatalf("level %d hp=%d err=%v want=%d", level, got, err, want)
		}
	}
	if _, err := d.HP(0); err == nil {
		t.Fatal("zero level accepted")
	}
	if _, err := d.HP(31); err == nil {
		t.Fatal("outside design level accepted")
	}
}
func TestMonsterHuntInstalledDesign23510(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	d, err := LoadMonsterHunt(root, "20260923193640", 1)
	if err != nil {
		t.Fatal(err)
	}
	if d.PackID != 1001 || d.DeckID != 10001 || d.MaxLevel != 30 || len(d.Rewards) != 30 {
		t.Fatalf("unexpected installed design %+v", d)
	}
	hp, err := d.HP(1)
	if err != nil || hp != 6600 {
		t.Fatalf("base HP %d %v", hp, err)
	}
	r := d.Rewards[1]
	if len(r.Clear) != 1 || r.Clear[0].Type != 8 || r.Clear[0].Count != 5 || len(r.Daily) != 1 || r.Daily[0].Type != 4 || r.Daily[0].Count != 20000 {
		t.Fatalf("unexpected rewards %+v", r)
	}
	p, err := LoadMonsterHuntPresetDesign(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	if p.BaseCount != 5 || p.Maximum != 10 || p.Price != 2000 || p.PriceType != 4 {
		t.Fatalf("unexpected hunt presets %+v", p)
	}
}
