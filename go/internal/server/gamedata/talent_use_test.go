package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"encoding/binary"
	"math"
	"testing"
)

func TestTalentUseLoaderJoinsCharacterLevelRewardsAndFood(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, table := range []string{"TalentTable", "CharTable", "TalentRewardTable", "FoodBuffTable", "FoodTable"} {
		if _, err = db.Exec("CREATE TABLE " + table + "(id INTEGER,ProtoBuf BLOB)"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("CREATE TABLE TalentSkillTable(groupId INTEGER,id INTEGER,ProtoBuf BLOB)"); err != nil {
		t.Fatal(err)
	}
	talent := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 18, 42), 11, 5), 1, 22)
	for _, v := range []struct {
		table string
		id    uint64
		b     []byte
	}{{"TalentTable", 8, talent}, {"CharTable", 350, wire.AppendVarint(nil, 18, 8)}, {"TalentRewardTable", 9, wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 2, 3), 3, 11), 4, 8)}, {"FoodBuffTable", 7, wire.AppendVarint(wire.AppendVarint(nil, 2, 1), 3, 42)}, {"FoodTable", 22, wire.AppendVarint(nil, 3, 7)}} {
		if _, err = db.Exec("INSERT INTO "+v.table+" VALUES(?,?)", v.id, v.b); err != nil {
			t.Fatal(err)
		}
	}
	var values []byte
	values = binary.LittleEndian.AppendUint32(values, math.Float32bits(0.75))
	values = binary.LittleEndian.AppendUint32(values, math.Float32bits(5))
	skill := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 10), 2, 13), 5, 2)
	skill = wire.AppendBytes(skill, 14, values)
	if _, err = db.Exec("INSERT INTO TalentSkillTable VALUES(?,?,?)", 42, 1, skill); err != nil {
		t.Fatal(err)
	}
	d, err := loadTalentUseDesign(db)
	if err != nil {
		t.Fatal(err)
	}
	meta := d.Characters[350]
	r := d.Rules[[2]uint64{42, 1}]
	if meta.Group != 42 || !meta.BannedPacks[22] || r.Class != 13 || r.Catalyst != 10 || r.Values[0] != 0.75 || r.Values[1] != 5 || d.Foods[22] != 42 || d.Rewards[9][0].Count != 3 {
		t.Fatal("talent loader lost static rule joins")
	}
	if _, err = db.Exec("UPDATE TalentSkillTable SET ProtoBuf=?", wire.AppendBytes(wire.AppendVarint(nil, 2, 13), 14, []byte{1, 2, 3})); err != nil {
		t.Fatal(err)
	}
	if _, err = loadTalentUseDesign(db); err == nil {
		t.Fatal("malformed float rule accepted")
	}
}
