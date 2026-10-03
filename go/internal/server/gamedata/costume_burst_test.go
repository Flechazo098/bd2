package gamedata

import (
	"database/sql"
	"os"
	"strings"
	"testing"
)

func TestCostumeBurstDesignAndUpgradeRule(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE CostumeBurstTable (groupId INTEGER,id INTEGER,ProtoBuf BLOB,PRIMARY KEY(groupId,id))`); err != nil {
		t.Fatal(err)
	}
	for level := 1; level <= 3; level++ {
		raw := burstProto(8, 42, 9, uint64(level), 10, 60, 11, 710, 12, 8)
		if _, err := db.Exec(`INSERT INTO CostumeBurstTable(groupId,id,ProtoBuf) VALUES(?,?,?)`, 42, level, raw); err != nil {
			t.Fatal(err)
		}
	}
	design, err := loadCostumeBurstDesign(db)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := design.UpgradeRule(42, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rule.NextLevel != 2 || rule.MaxLevel != 3 || len(rule.Costs) != 1 || rule.Costs[0] != (PromotionCost{Type: 8, ID: 710, Count: 60}) {
		t.Fatalf("unexpected rule: %+v", rule)
	}
	if _, err := design.UpgradeRule(42, 3); err == nil {
		t.Fatal("expected max-level error")
	}
}

func TestCostumeBurstRejectsInvalidIdentityCostAndGap(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		rows [][2]int
	}{
		{"identity", burstProto(8, 99, 9, 1, 10, 1, 11, 710, 12, 8), [][2]int{{42, 1}}},
		{"zero-count", burstProto(8, 42, 9, 1, 10, 0, 11, 710, 12, 8), [][2]int{{42, 1}}},
		{"gold-id", burstProto(8, 42, 9, 1, 10, 1, 11, 710, 12, 4), [][2]int{{42, 1}}},
		{"gap", burstProto(8, 42, 9, 2, 10, 1, 11, 710, 12, 8), [][2]int{{42, 2}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE CostumeBurstTable (groupId INTEGER,id INTEGER,ProtoBuf BLOB,PRIMARY KEY(groupId,id))`); err != nil {
				t.Fatal(err)
			}
			for _, row := range tc.rows {
				if _, err := db.Exec(`INSERT INTO CostumeBurstTable(groupId,id,ProtoBuf) VALUES(?,?,?)`, row[0], row[1], tc.raw); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := loadCostumeBurstDesign(db); err == nil || !strings.Contains(err.Error(), "costume burst") {
				t.Fatalf("expected costume burst validation error, got %v", err)
			}
		})
	}
}

func TestCostumeBurstAgainstInstalledCurrentVersion(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("set BD2_REAL_GAMEDATA for installed GameData integration test")
	}
	design, err := LoadCostumeBurstDesign(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	if len(design.Levels) != 56 {
		t.Fatalf("installed burst groups=%d, want 56", len(design.Levels))
	}
	rows := 0
	for _, levels := range design.Levels {
		rows += len(levels)
	}
	if rows != 168 {
		t.Fatalf("installed burst rows=%d, want 168", rows)
	}
	rule, err := design.UpgradeRule(4202, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []PromotionCost{{Type: 4, Count: 1_500_000}, {Type: 8, ID: 710, Count: 60}}
	if rule.NextLevel != 1 || rule.MaxLevel != 3 || len(rule.Costs) != len(want) || rule.Costs[0] != want[0] || rule.Costs[1] != want[1] {
		t.Fatalf("installed rule=%+v", rule)
	}
}

// burstProto builds the small varint-only fields used by the loader tests.
func burstProto(fields ...uint64) []byte {
	if len(fields)%2 != 0 {
		panic("burstProto requires field/value pairs")
	}
	var out []byte
	for i := 0; i < len(fields); i += 2 {
		field, value := fields[i], fields[i+1]
		out = append(out, byte(field<<3))
		for value >= 0x80 {
			out = append(out, byte(value)|0x80)
			value >>= 7
		}
		out = append(out, byte(value))
	}
	return out
}
