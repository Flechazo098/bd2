package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"testing"
)

func TestCashEntitlementDesignUsesTicketTypesAndAttendanceMapping(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"AvatarSetTable", "ContentTicketTable", "ContentOpenTable", "AttendanceRewardTable"} {
		if _, err = db.Exec("CREATE TABLE " + table + "(ProtoBuf BLOB)"); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec("INSERT INTO AvatarSetTable VALUES(?)", wire.AppendVarint(nil, 6, 10))
	raw := wire.AppendVarint(nil, 4, 38)
	raw = wire.AppendVarint(raw, 8, 2)
	db.Exec("INSERT INTO ContentTicketTable VALUES(?)", raw)
	raw = wire.AppendVarint(nil, 6, 38)
	raw = wire.AppendVarint(raw, 2, 1)
	db.Exec("INSERT INTO ContentOpenTable VALUES(?)", raw)
	for _, id := range []uint64{2, 1} {
		raw = wire.AppendVarint(nil, 1, 38)
		raw = wire.AppendVarint(raw, 2, id)
		raw = wire.AppendVarint(raw, 3, 60)
		raw = wire.AppendVarint(raw, 5, 3)
		db.Exec("INSERT INTO AttendanceRewardTable VALUES(?)", raw)
	}
	d, err := loadCashEntitlementDesign(db)
	if err != nil || !d.AvatarSets[10] || d.TicketTypes[38] != 2 || d.AttendanceTypes[38] != 1 || len(d.Attendance[38]) != 2 || d.Attendance[38][0].ID != 1 {
		t.Fatalf("%+v %v", d, err)
	}
}
