package calendar

import (
	"bd2server/internal/server/readonly"
	"bd2server/internal/server/wire"
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, dir, name string, m Manifest) {
	t.Helper()
	raw, e := MarshalBinary(m)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, name), raw, 0600); e != nil {
		t.Fatal(e)
	}
}
func baseManifest() Manifest {
	return Manifest{SchemaVersion: 1, Revision: "2026-10-05", GameVersion: "2.35.10", GameDataVersion: "2.35.10"}
}
func TestInstalledCalendarsContainAllRuntimeDomains(t *testing.T) {
	set, e := LoadDirectory("../../../../schedules", "2.35.10", "20260923193640")
	if e != nil {
		t.Fatal(e)
	}
	if len(set.Events) != 54 || len(set.GachaSeed.Schedules) != 11 || len(set.GachaSeed.StepUps) != 2 || set.RegularService == nil || len(set.RegularService.Contents) != 9 || set.MonsterHunt == nil || len(set.MonsterHunt.History) != 78 || len(set.CashProducts) != 37 || len(set.EventHubs) != 2 || len(set.MiniGameHubs) != 6 {
		t.Fatalf("installed calendar omitted a domain: events=%d gacha=%d steps=%d cash=%d hubs=%d mini=%d", len(set.Events), len(set.GachaSeed.Schedules), len(set.GachaSeed.StepUps), len(set.CashProducts), len(set.EventHubs), len(set.MiniGameHubs))
	}
}
func TestMultiFileCalendarAtomicDeterministicAndIndependentRevision(t *testing.T) {
	dir := t.TempDir()
	a := baseManifest()
	a.Revision = "a"
	a.Gacha = []Gacha{{GroupID: 9, Start: "2026-10-01T00:00:00Z", End: "2026-10-02T00:00:00Z"}}
	writeManifest(t, dir, "z.bd2schedule", a)
	b := baseManifest()
	b.Revision = "b"
	b.Gacha = []Gacha{{GroupID: 1, Start: "2027-01-01T09:00:00+09:00", End: "2027-01-02T09:00:00+09:00"}, {GroupID: 9, Start: "2026-10-02T00:00:00Z", End: "2026-10-03T00:00:00Z"}}
	writeManifest(t, dir, "a.bd2schedule", b)
	set, e := LoadDirectory(dir, "2.35.10", "2.35.10")
	if e != nil {
		t.Fatal(e)
	}
	if len(set.GachaSeed.Schedules) != 3 || set.GachaSeed.Schedules[0].GroupID != 1 || set.Revisions[0] != "b" {
		t.Fatalf("unexpected merged set %+v", set)
	}
	b.Revision = "changed without game update"
	b.Gacha[0].End = "2027-01-03T09:00:00+09:00"
	writeManifest(t, dir, "a.bd2schedule", b)
	changed, e := LoadDirectory(dir, "2.35.10", "2.35.10")
	if e != nil || changed.GachaSeed.Schedules[0].EndTime == set.GachaSeed.Schedules[0].EndTime {
		t.Fatalf("calendar update ignored: %v", e)
	}
	if e = os.WriteFile(filepath.Join(dir, "broken.bd2schedule"), []byte("broken binary"), 0600); e != nil {
		t.Fatal(e)
	}
	if partial, e := LoadDirectory(dir, "2.35.10", "2.35.10"); e == nil || partial != nil {
		t.Fatal("bad file returned partial calendar")
	}
}
func TestStrictCalendarRejectsConflictsAndInvalidData(t *testing.T) {
	for _, mutate := range []func(*Manifest){func(m *Manifest) { m.GameDataVersion = "wrong" }, func(m *Manifest) { m.Gacha[0].Start = "2026-10-01" }, func(m *Manifest) { m.Gacha[0].End = m.Gacha[0].Start }, func(m *Manifest) { m.Gacha = append(m.Gacha, m.Gacha[0]) }, func(m *Manifest) { m.Gacha[0].GroupID = 1 << 32 }} {
		dir := t.TempDir()
		m := baseManifest()
		m.Gacha = []Gacha{{GroupID: 1, Start: "2026-10-01T00:00:00Z", End: "2026-10-02T00:00:00Z"}}
		mutate(&m)
		writeManifest(t, dir, "one.bd2schedule", m)
		if _, e := LoadDirectory(dir, "2.35.10", "2.35.10"); e == nil {
			t.Fatal("invalid calendar accepted")
		}
	}
	dir := t.TempDir()
	m := baseManifest()
	m.Events = []Event{{UID: 77, Type: 12, ID: 3, Start: "2026-10-01T00:00:00Z", End: "2026-10-02T00:00:00Z"}}
	writeManifest(t, dir, "a.bd2schedule", m)
	writeManifest(t, dir, "b.bd2schedule", m)
	if _, e := LoadDirectory(dir, "2.35.10", "2.35.10"); e == nil {
		t.Fatal("duplicate UID across files accepted")
	}
}
func TestReadonlyCalendarReplacementPreservesStaticResponses(t *testing.T) {
	set := &Set{CashProducts: []CashProduct{{GroupID: 11, ProductID: 22, EventIndex: 99}}, MonsterHunt: &MonsterHunt{StartRegularSeason: 5, History: []HuntHistory{{Season: 78, HuntID: 73, Hidden: true}}}}
	old := &readonly.Seed{Version: "2.35.10", Responses: map[string]readonly.Response{"/Static": {PacketCode: 17, Fields: []readonly.Field{scalar(1, 10)}}, "/CashShopInfo": {PacketCode: 60, Fields: []readonly.Field{scalar(2, 1)}}}}
	result, e := set.ApplyReadonly(old)
	if e != nil {
		t.Fatal(e)
	}
	if len(old.Responses["/CashShopInfo"].Fields) != 1 || result.Responses["/Static"].PacketCode != 17 {
		t.Fatal("source seed changed/static endpoint lost")
	}
	_, raw, handled, e := result.Handle("/CashShopInfo", wire.AppendVarint(nil, 1, 1))
	if e != nil || !handled {
		t.Fatal(e)
	}
	p, found, e := wire.Bytes(raw, 1)
	if e != nil || !found {
		t.Fatal("missing product")
	}
	id, _, _ := wire.Varint(p, 8)
	if id != 99 {
		t.Fatalf("event index=%d", id)
	}
	if _, found, _ = wire.Varint(raw, 2); found {
		t.Fatal("stale reset timestamp retained")
	}
}
func TestInstalledCalendarRealGameData(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	set, e := LoadDirectory("../../../../schedules", "2.35.10", "20260923193640")
	if e != nil {
		t.Fatal(e)
	}
	if e = set.ValidateDesign(root, "20260923193640"); e != nil {
		t.Fatal(e)
	}
}
