package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"maps"
	"strings"
	"testing"
)

func TestRecruitSpecialDefaultsUseProtoZeroAndValidateConfiguration(t *testing.T) {
	fields := map[int][]uint64{1: {2}, 2: {120}, 4: {50}, 6: {3}, 7: {99}}
	d := RecruitDesign{}
	if e := decodeRecruitSpecialDefaults(friendshipTestProto(fields), &d); e != nil {
		t.Fatal(e)
	}
	if d.AppearCount != 2 || d.AutoResetMinute != 120 || d.ResetCount != 50 || d.ResetType != 3 || d.ResetLimit != 99 {
		t.Fatalf("defaults %+v", d)
	}
	for _, field := range []int{1, 2, 4, 6, 7} {
		copyFields := map[int][]uint64{}
		maps.Copy(copyFields, fields)
		delete(copyFields, field)
		if e := decodeRecruitSpecialDefaults(friendshipTestProto(copyFields), &RecruitDesign{}); e == nil {
			t.Fatalf("missing configuration field %d accepted", field)
		}
	}
	fields[5] = []uint64{1}
	if e := decodeRecruitSpecialDefaults(friendshipTestProto(fields), &RecruitDesign{}); e == nil {
		t.Fatal("currency item ID accepted")
	}
}

// Invalid rows must fail before any character/costume lookup can hide the
// actual malformed recruitment table behind a missing unrelated table.
func TestRecruitLoaderRejectsInvalidProtocolRows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[int][]uint64
	}{
		{"identity", map[int][]uint64{2: {101}, 3: {11}, 5: {1}, 6: {71}, 7: {8}}},
		{"partial materials", map[int][]uint64{2: {101}, 3: {10}, 5: {1}, 7: {8}}},
		{"zero cost", map[int][]uint64{2: {101}, 3: {10}, 5: {0}, 6: {71}, 7: {8}}},
		{"unsupported currency", map[int][]uint64{2: {101}, 3: {10}, 5: {1}, 6: {71}, 7: {3}}},
		{"unsupported type", map[int][]uint64{2: {101}, 3: {10}, 5: {1}, 6: {71}, 7: {8}, 9: {2}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, e := sql.Open("sqlite", ":memory:")
			if e != nil {
				t.Fatal(e)
			}
			defer func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			}()
			if _, e = db.Exec("CREATE TABLE MercenaryScoutTable(id INTEGER,ProtoBuf BLOB)"); e != nil {
				t.Fatal(e)
			}
			var proto []byte
			for field, values := range tc.fields {
				for _, v := range values {
					proto = wire.AppendVarint(proto, field, v)
				}
			}
			if _, e = db.Exec("INSERT INTO MercenaryScoutTable VALUES(10,?)", proto); e != nil {
				t.Fatal(e)
			}
			_, e = loadRecruitDesign(db)
			if e == nil || strings.Contains(e.Error(), "no such table") {
				t.Fatalf("bad row not rejected by recruitment decoder: %v", e)
			}
		})
	}
}
