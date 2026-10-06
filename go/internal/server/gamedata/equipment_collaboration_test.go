package gamedata

import (
	"database/sql"
	"encoding/binary"
	"maps"
	"os"
	"reflect"
	"testing"
)

func TestIncludeCollaborationURWeaponsInstalled(t *testing.T) {
	root := os.Getenv("BD2_TEST_GAMEDATA_ROOT")
	if root == "" {
		t.Skip("set BD2_TEST_GAMEDATA_ROOT for installed GameData integration test")
	}
	c, err := LoadEquipmentGachaGroups(root, "20260923193640", []uint64{10002, 9, 133, 206})
	if err != nil {
		t.Fatal(err)
	}
	before := map[uint64]EquipmentGacha{}
	maps.Copy(before, c.Gachas)
	if err := c.IncludeCollaborationURWeapons(root, "20260923193640"); err != nil {
		t.Fatal(err)
	}
	g := c.Gachas[71200001]
	oldIDs := map[uint64]bool{}
	for _, branch := range before[g.ID].Pool {
		for _, item := range branch.Children {
			oldIDs[item.ID] = true
		}
	}
	added := 0
	for _, branch := range g.Pool {
		for _, item := range branch.Children {
			if !oldIDs[item.ID] {
				added++
				if item.ID < 943200 || item.ID > 943213 {
					t.Fatalf("unexpected added collaboration weapon %d", item.ID)
				}
			}
		}
	}
	if len(oldIDs) != 73 || added != 14 {
		t.Fatalf("original=%d added=%d", len(oldIDs), added)
	}
	for index, want := range []int{64, 9, 14} {
		if len(g.Pool[index].Children) != want || g.Pool[index].Weight != before[g.ID].Pool[index].Weight {
			t.Fatalf("branch %d: %+v", index, g.Pool[index])
		}
	}
	if len(before[g.ID].Pool[0].Children) != 50 {
		t.Fatal("original pool mutated")
	}
	for id, old := range before {
		if id != g.ID && !reflect.DeepEqual(old, c.Gachas[id]) && !old.TicketOnly {
			t.Fatalf("other product %d changed", id)
		}
	}
	// The same UR weapon ticket design is available in a one-item product.
	// Its semantic eligibility cannot depend on one versus ten draw IDs.
	one := c.Gachas[70200001]
	if len(one.Pool) == 0 || len(one.Pool[0].Children) != len(g.Pool[0].Children) {
		t.Fatal("equivalent one-ticket UR pool not extended")
	}
	for _, branch := range g.Pool {
		for _, item := range branch.Children {
			if _, _, _, err := c.RollOptions(item.ID); err != nil {
				t.Fatalf("options %d: %v", item.ID, err)
			}
		}
	}
	if err := c.IncludeCollaborationURWeapons(root, "20260923193640"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, c.Gachas[g.ID]) {
		t.Fatal("second inclusion changed pool")
	}
}

func TestIncludeCollaborationURWeaponsSynthetic(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	db.SetMaxOpenConns(1)
	for _, schema := range []string{"CREATE TABLE LimitedCostumeTable(id INTEGER)", "CREATE TABLE CostumeTable(id INTEGER,useUniqueCharId INTEGER,ProtoBuf BLOB)", "CREATE TABLE CharTable(id INTEGER,uniqueCharId INTEGER,ProtoBuf BLOB)", "CREATE TABLE EquipmentTable(id INTEGER,privateUniqueCharId INTEGER,ProtoBuf BLOB)", "CREATE TABLE EquipmentOptionTable(id INTEGER,GroupId INTEGER,ProtoBuf BLOB)"} {
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	proto := func(values ...uint64) []byte {
		var b []byte
		for i := 0; i < len(values); i += 2 {
			b = binary.AppendUvarint(b, values[i]<<3)
			b = binary.AppendUvarint(b, values[i+1])
		}
		return b
	}
	for i := uint64(1); i <= 4; i++ {
		grade := uint64(6 - i)
		if i == 4 {
			grade = 5
		}
		if _, err := db.Exec("INSERT INTO CharTable VALUES(?,?,?)", i, i, proto(20, i, 9, grade)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO EquipmentTable VALUES(?,?,?)", 100+i, i, proto(6, 100+i, 16, i, 3, 4, 18, 3)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO LimitedCostumeTable VALUES(40),(41)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO CostumeTable VALUES(40,4,?),(41,4,?)", proto(27, 4), proto(27, 4)); err != nil {
		t.Fatal(err)
	}
	pool := []WeightedEquipment{{Weight: 150, Children: []WeightedEquipment{{ID: 101, Weight: 1}}}, {Weight: 350, Children: []WeightedEquipment{{ID: 102, Weight: 1}}}, {Weight: 500, Children: []WeightedEquipment{{ID: 103, Weight: 1}}}}
	c := &EquipmentGachaCatalog{Gachas: map[uint64]EquipmentGacha{880077: {ID: 880077, TicketOnly: true, TicketIDs: []uint64{990055}, Pool: pool}, 77: {ID: 77, Pool: pool}}, equipment: map[uint64]EquipmentDesign{}}
	if err := c.includeCollaborationURWeapons(db); err != nil {
		t.Fatal(err)
	}
	if len(c.Gachas[880077].Pool[0].Children) != 2 || len(c.Gachas[77].Pool[0].Children) != 1 {
		t.Fatal("extension or isolation failed")
	}
	if _, ok := c.equipment[104]; !ok {
		t.Fatal("new equipment design not loaded")
	}
	if err := c.includeCollaborationURWeapons(db); err != nil {
		t.Fatal(err)
	}
	if len(c.Gachas[880077].Pool[0].Children) != 2 {
		t.Fatal("duplicate owner or repeated inclusion duplicated equipment")
	}
	// Eligibility follows the product definitions even when there are fewer
	// character rarity branches than the current installed three-tier pool.
	c.Gachas[990088] = EquipmentGacha{ID: 990088, TicketOnly: true, TicketIDs: []uint64{554433}, Pool: pool[:1]}
	if err := c.includeCollaborationURWeapons(db); err != nil {
		t.Fatal(err)
	}
	if len(c.Gachas[990088].Pool[0].Children) != 2 {
		t.Fatal("changed product/ticket/branch count ignored")
	}
	delete(c.Gachas, 990088)
	if _, err := db.Exec("UPDATE CharTable SET ProtoBuf=? WHERE id=4", proto(20, 4, 9, 2)); err != nil {
		t.Fatal(err)
	}
	delete(c.Gachas, 880077)
	c.Gachas[880077] = EquipmentGacha{ID: 880077, TicketOnly: true, TicketIDs: []uint64{990055}, Pool: pool}
	if err := c.includeCollaborationURWeapons(db); err == nil {
		t.Fatal("unknown tier accepted")
	}
	if len(c.Gachas[880077].Pool[0].Children) != 1 {
		t.Fatal("failed inclusion published partial pool")
	}
	invalid := c.Gachas[880077]
	invalid.Pool = append([]WeightedEquipment(nil), pool...)
	invalid.Pool[0].Children = []WeightedEquipment{{ID: 101, Weight: 1}, {ID: 101, Weight: 1}}
	c.Gachas[880077] = invalid
	if err := c.includeCollaborationURWeapons(db); err == nil {
		t.Fatal("duplicate existing candidate accepted")
	}
}

func TestEquipmentFixedUsesChangedIDThresholdsAndResetDefault(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := db.Exec("CREATE TABLE GachaFixedTable(id INTEGER,ProtoBuf BLOB)"); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	for _, v := range []uint64{8, 7, 24, 43} {
		raw = binary.AppendUvarint(raw, v)
	}
	if _, err := db.Exec("INSERT INTO GachaFixedTable VALUES(?,?)", 987, raw); err != nil {
		t.Fatal(err)
	}
	fixed, err := loadEquipmentFixed(db, 987)
	if err != nil || fixed.ID != 987 || fixed.SRCount != 7 || fixed.URCount != 43 || fixed.Reset {
		t.Fatalf("dynamic fixed=%+v err=%v", fixed, err)
	}
	raw = append(raw, 48, 1)
	if _, err := db.Exec("UPDATE GachaFixedTable SET ProtoBuf=?", raw); err != nil {
		t.Fatal(err)
	}
	fixed, err = loadEquipmentFixed(db, 987)
	if err != nil || !fixed.Reset {
		t.Fatalf("reset=%+v err=%v", fixed, err)
	}
}

func TestEquipmentFixedProgramsFollowEachGroup(t *testing.T) {
	c := &EquipmentGachaCatalog{groups: map[uint64]EquipmentGachaGroup{71: {ID: 71, FixedID: 901}, 72: {ID: 72, FixedID: 902}}, fixedDesigns: map[uint64]EquipmentFixedDesign{901: {ID: 901, SRCount: 7, URCount: 43}, 902: {ID: 902, SRCount: 2, URCount: 5, Reset: true}}}
	for _, pair := range []struct{ group, fixed, threshold uint64 }{{71, 901, 43}, {72, 902, 5}} {
		fixed, ok := c.FixedForGroup(pair.group)
		if !ok || fixed.ID != pair.fixed || fixed.URCount != pair.threshold {
			t.Fatalf("group%d fixed=%+v found=%v", pair.group, fixed, ok)
		}
	}
	if _, ok := c.FixedForGroup(73); ok {
		t.Fatal("unknown group inherited another fixed program")
	}
	programs := c.FixedDesigns()
	if len(programs) != 2 || programs[0].ID != 901 || programs[1].ID != 902 {
		t.Fatalf("fixed designs=%+v", programs)
	}
}

func TestEquipmentPityUsesActualGradesAndReorderedNestedPool(t *testing.T) {
	g := EquipmentGacha{Count: 1, Grades: map[uint64]uint64{90: 2, 80: 4, 70: 3}, Pool: []WeightedEquipment{{ID: 90, Weight: 60}, {Weight: 30, Children: []WeightedEquipment{{ID: 80, Weight: 10}, {ID: 70, Weight: 20}}}}}
	fixed := EquipmentFixedDesign{ID: 99, SRCount: 7, URCount: 43, Reset: true}
	draw := func(limit uint64) (uint64, error) { return 0, nil }
	got, state, err := g.rollWith(6, 42, fixed, draw)
	if err != nil || len(got) != 1 || got[0] != 80 || state.URSort != 0 || state.SRCount != 0 || state.URCount != 0 {
		t.Fatalf("UR actualgrade got=%v state=%+v err=%v", got, state, err)
	}
	got, state, err = g.rollWith(6, 0, fixed, draw)
	if err != nil || got[0] != 70 || state.SRSort != 0 || state.SRCount != 0 || state.URCount != 1 {
		t.Fatalf("SR actualgrade got=%v state=%+v err=%v", got, state, err)
	}
	fixed.Reset = false
	got, state, err = g.rollWith(6, 42, fixed, draw)
	if err != nil || got[0] != 80 || state.SRCount != 0 || state.URCount != 0 {
		t.Fatalf("forced nonreset pity=%v state=%+v err=%v", got, state, err)
	}
	g.Pool = []WeightedEquipment{{ID: 80, Weight: 1}}
	got, state, err = g.rollWith(2, 3, fixed, draw)
	if err != nil || got[0] != 80 || state.SRCount != 3 || state.URCount != 4 {
		t.Fatalf("natural UR nonreset=%v state=%+v err=%v", got, state, err)
	}
}

func TestEquipmentConditionalGradePreservesMixedBranchProbability(t *testing.T) {
	g := EquipmentGacha{Grades: map[uint64]uint64{1: 4, 2: 2, 3: 4}, Pool: []WeightedEquipment{{Weight: 1, Children: []WeightedEquipment{{ID: 1, Weight: 1}, {ID: 2, Weight: 3}}}, {Weight: 1, Children: []WeightedEquipment{{ID: 3, Weight: 1}}}}}
	pool, err := g.gradePool(4)
	if err != nil || len(pool) != 2 || pool[0].ID != 1 || pool[1].ID != 3 || pool[1].Weight != 4*pool[0].Weight {
		t.Fatalf("mixed conditional distribution=%+v err=%v", pool, err)
	}
}

func TestEquipmentGachaLoaderAcceptsChangedPriceAndPoolLength(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, table := range []string{"GachaTable", "RewardGroupTable"} {
		if _, err := db.Exec("CREATE TABLE " + table + "(id INTEGER,ProtoBuf BLOB)"); err != nil {
			t.Fatal(err)
		}
	}
	proto := func(values ...uint64) []byte {
		var raw []byte
		for i := 0; i < len(values); i += 2 {
			raw = binary.AppendUvarint(raw, values[i]<<3)
			raw = binary.AppendUvarint(raw, values[i+1])
		}
		return raw
	}
	if _, err := db.Exec("INSERT INTO GachaTable VALUES(?,?)", 808, proto(5, 2, 7, 909, 10, 666, 12, 3)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO RewardGroupTable VALUES(?,?)", 909, proto(5, 707, 6, 10, 8, 123)); err != nil {
		t.Fatal(err)
	}
	g, err := loadEquipmentGacha(db, 808)
	if err != nil || g.Count != 2 || g.Price != 666 || len(g.Pool) != 1 || g.Pool[0].Weight != 123 {
		t.Fatalf("changed price/pool=%+v err=%v", g, err)
	}
}
