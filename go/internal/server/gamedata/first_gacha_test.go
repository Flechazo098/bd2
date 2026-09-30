package gamedata

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"bd2server/internal/server/wire"
)

func firstGachaTestProgram() *FirstGachaRewardGroup {
	return &FirstGachaRewardGroup{ID: 77, DropCount: 1, DropType: 1, Entries: []FirstGachaRewardEntry{
		{ItemType: 11, ItemID: 5001, Count: 1, Weight: 1},
		{ItemType: 9, ItemID: 78, Count: 1, Weight: 1, Group: &FirstGachaRewardGroup{ID: 78, DropCount: 2, Entries: []FirstGachaRewardEntry{
			{ItemType: 10, ItemID: 1001, Count: 1, Weight: 25},
			{ItemType: 10, ItemID: 1002, Count: 1, Weight: 75},
		}}},
	}}
}

func TestFirstGachaMixedProgramPreservesCountsAndBranchWeights(t *testing.T) {
	design, err := NewFirstGachaDesign(GachaGroupDesign{ID: 2, GachaSubType: 3, TenTimeGachaID: 20}, 20, 3, firstGachaTestProgram(),
		map[uint64]CharacterDesign{5001: {ID: 500, HP: 100}},
		map[uint64]EquipmentDesign{1001: {ID: 1001, Grade: 2}, 1002: {ID: 1002, Grade: 3}})
	if err != nil {
		t.Fatal(err)
	}
	values := []uint64{24, 25}
	roll, err := design.roll(func(limit uint64) (uint64, error) {
		if limit != 100 {
			t.Fatalf("weight total=%d want=100", limit)
		}
		value := values[0]
		values = values[1:]
		return value, nil
	})
	if err != nil || len(roll) != 3 || roll[0] != (FirstGachaReward{Type: 11, ID: 5001}) || roll[1].ID != 1001 || roll[2].ID != 1002 {
		t.Fatalf("roll=%v err=%v", roll, err)
	}
	if len(design.CostumeCatalog().Gachas) != 0 {
		t.Fatal("starter design exposed a zero-price ordinary product")
	}
	if _, ok := design.Character(5001); !ok {
		t.Fatal("starter character metadata missing")
	}
}

func TestFirstGachaRejectsInvalidPrograms(t *testing.T) {
	for _, kind := range []string{"cycle", "unsupported type", "mismatched weighted count", "zero weight", "excessive count"} {
		t.Run(kind, func(t *testing.T) {
			g := firstGachaTestProgram()
			switch kind {
			case "cycle":
				g.Entries[1].ItemID = g.ID
				g.Entries[1].Group = g
			case "unsupported type":
				g.Entries[0].ItemType = 8
			case "mismatched weighted count":
				g.Entries[1].Group.Entries[0].Count = 2
			case "zero weight":
				g.Entries[0].Weight = 0
			case "excessive count":
				g.Entries[1].Group.DropCount = 101
			}
			if _, err := firstGachaRewardCount(g, map[*FirstGachaRewardGroup]bool{}); err == nil {
				t.Fatal("invalid mixed reward program accepted")
			}
		})
	}
}

func TestFirstGachaLoaderRejectsNonStarterOrPricedMetadata(t *testing.T) {
	for _, bad := range []string{"group subtype", "group type", "cash product", "scheduled", "priced", "ticket", "daily allowance"} {
		t.Run(bad, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			for _, table := range []string{"GameDefaultTable", "GachaGroupTable", "GachaTable"} {
				if _, err := db.Exec("CREATE TABLE " + table + " (id INTEGER PRIMARY KEY,ProtoBuf BLOB)"); err != nil {
					t.Fatal(err)
				}
			}
			group := wire.AppendVarint(nil, 18, 2)
			group = wire.AppendVarint(group, 16, 3)
			group = wire.AppendVarint(group, 33, 20)
			product := wire.AppendVarint(nil, 9, 20)
			product = wire.AppendVarint(product, 5, 10)
			product = wire.AppendVarint(product, 7, 20)
			switch bad {
			case "group subtype":
				group, _, err = wire.ReplaceVarint(group, 16, 1)
			case "group type":
				group = wire.AppendVarint(group, 17, 1)
			case "cash product":
				group = wire.AppendVarint(group, 3, 123)
			case "scheduled":
				group = wire.AppendVarint(group, 28, 1)
			case "priced":
				product = wire.AppendVarint(product, 10, 200)
			case "ticket":
				product = wire.AppendVarint(product, 8, 450000)
			case "daily allowance":
				product = wire.AppendVarint(product, 4, 1)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range []struct {
				table string
				id    uint64
				raw   []byte
			}{{"GameDefaultTable", 0, wire.AppendVarint(nil, 46, 2)}, {"GachaGroupTable", 2, group}, {"GachaTable", 20, product}} {
				if _, err := db.Exec("INSERT INTO "+row.table+" VALUES (?,?)", row.id, row.raw); err != nil {
					t.Fatal(err)
				}
			}
			_, err = loadFirstGacha(db)
			want := "unsupported field"
			if bad == "ticket" {
				want = "cannot consume tickets"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("metadata was not rejected before reading rewards: err=%v want=%q", err, want)
			}
		})
	}
}

func TestFirstGachaAgainstInstalledVersion23510(t *testing.T) {
	root := os.Getenv("BD2_TEST_GAMEDATA_ROOT")
	if root == "" {
		t.Skip("set BD2_TEST_GAMEDATA_ROOT for installed GameData integration test")
	}
	design, err := LoadFirstGacha(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	if design.Group.ID != 2 || design.GachaID != 20 || design.Count != 10 || design.Group.PointCount != 0 || len(design.RewardGroup.Entries) != 6 {
		t.Fatalf("design=%+v", design)
	}
	// Current GameData gives one five-star, one four-star, three three-star
	// costumes and five exclusive-equipment instances in every reroll.
	for i := 0; i < 20; i++ {
		roll, err := design.Roll()
		if err != nil {
			t.Fatal(err)
		}
		var costumes, equipment int
		for _, reward := range roll {
			switch reward.Type {
			case 11:
				costumes++
				if _, ok := design.Character(reward.ID); !ok {
					t.Fatalf("unknown costume %d", reward.ID)
				}
			case 10:
				equipment++
				if _, ok := design.EquipmentCatalog().equipment[reward.ID]; !ok {
					t.Fatalf("unknown equipment %d", reward.ID)
				}
			default:
				t.Fatalf("unexpected reward type %d", reward.Type)
			}
		}
		if len(roll) != 10 || costumes != 5 || equipment != 5 {
			t.Fatalf("mixed draw has costumes=%d equipment=%d results=%v", costumes, equipment, roll)
		}
	}
}
