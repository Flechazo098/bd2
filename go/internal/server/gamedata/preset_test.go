package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"testing"
)

func TestPresetDesignReadsChangedLimitsAndPrice(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TABLE GameDefaultTable(id INTEGER,ProtoBuf BLOB)"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE PresetTable(id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO PresetTable VALUES(79)"); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	for field, value := range map[int]uint64{91: 3, 92: 73, 94: 3, 95: 9} {
		raw = wire.AppendVarint(raw, field, value)
	}
	if _, err = db.Exec("INSERT INTO GameDefaultTable VALUES(0,?)", raw); err != nil {
		t.Fatal(err)
	}
	got, err := loadPresetDesign(db)
	if err != nil || got.BaseCount != 3 || got.Maximum != 9 || got.Price != 73 || got.PriceType != 3 || !got.Icons[79] {
		t.Fatalf("design=%+v err=%v", got, err)
	}
}
