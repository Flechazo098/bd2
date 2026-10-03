package gamedata

import (
	"database/sql"
	"encoding/binary"
	"os"
	"reflect"
	"testing"
)

func TestIncludeCollaborationURWeaponsInstalled(t *testing.T) {
	root := os.Getenv("BD2_TEST_GAMEDATA_ROOT")
	if root == "" {
		t.Skip("set BD2_TEST_GAMEDATA_ROOT for installed GameData integration test")
	}
	c, err := LoadEquipmentGacha(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	before := map[uint64]EquipmentGacha{}
	for id, g := range c.Gachas {
		before[id] = g
	}
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
		if id != g.ID && !reflect.DeepEqual(old, c.Gachas[id]) {
			t.Fatalf("other product %d changed", id)
		}
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
	defer db.Close()
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
	c := &EquipmentGachaCatalog{Gachas: map[uint64]EquipmentGacha{71200001: {ID: 71200001, TicketOnly: true, TicketIDs: []uint64{1104}, Pool: pool}, 77: {ID: 77, Pool: pool}}, equipment: map[uint64]EquipmentDesign{}}
	if err := c.includeCollaborationURWeapons(db); err != nil {
		t.Fatal(err)
	}
	if len(c.Gachas[71200001].Pool[0].Children) != 2 || len(c.Gachas[77].Pool[0].Children) != 1 {
		t.Fatal("extension or isolation failed")
	}
	if _, ok := c.equipment[104]; !ok {
		t.Fatal("new equipment design not loaded")
	}
	if err := c.includeCollaborationURWeapons(db); err != nil {
		t.Fatal(err)
	}
	if len(c.Gachas[71200001].Pool[0].Children) != 2 {
		t.Fatal("duplicate owner or repeated inclusion duplicated equipment")
	}
	if _, err := db.Exec("UPDATE CharTable SET ProtoBuf=? WHERE id=4", proto(20, 4, 9, 2)); err != nil {
		t.Fatal(err)
	}
	delete(c.Gachas, 71200001)
	c.Gachas[71200001] = EquipmentGacha{ID: 71200001, TicketOnly: true, TicketIDs: []uint64{1104}, Pool: pool}
	if err := c.includeCollaborationURWeapons(db); err == nil {
		t.Fatal("unknown tier accepted")
	}
	if len(c.Gachas[71200001].Pool[0].Children) != 1 {
		t.Fatal("failed inclusion published partial pool")
	}
	invalid := c.Gachas[71200001]
	invalid.Pool = append([]WeightedEquipment(nil), pool...)
	invalid.Pool[0].Children = []WeightedEquipment{{ID: 101, Weight: 1}, {ID: 101, Weight: 1}}
	c.Gachas[71200001] = invalid
	if err := c.includeCollaborationURWeapons(db); err == nil {
		t.Fatal("duplicate existing candidate accepted")
	}
}
