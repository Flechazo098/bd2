package events

import (
	"testing"
	"time"

	"bd2server/internal/server/wire"
)

func TestRegistryExplicitCalendarAndIsolation(t *testing.T) {
	r := NewRegistry()
	r.now = func() time.Time { return time.UnixMilli(100) }
	if len(r.List()) != 0 {
		t.Fatal("empty calendar activated events")
	}
	rows := []Schedule{{UID: 77, Type: 12, ID: 3, Start: 100, End: 200}}
	if err := r.Replace(rows); err != nil {
		t.Fatal(err)
	}
	rows[0].ID = 99
	got := r.List()
	got[0].ID = 88
	s, err := r.Resolve(77)
	if err != nil || s.ID != 3 {
		t.Fatalf("calendar aliased caller: %+v %v", s, err)
	}
	code, out, ok, err := r.Handle("/EventScheduleInfo", wire.AppendVarint(nil, 1, 1))
	if err != nil || !ok || code != 163 {
		t.Fatalf("schedule: %d %t %v", code, ok, err)
	}
	b, _, err := wire.Bytes(out, 1)
	if err != nil {
		t.Fatal(err)
	}
	if active, _, _ := wire.Varint(b, 7); active != 1 {
		t.Fatal("start boundary inactive")
	}
	r.now = func() time.Time { return time.UnixMilli(200) }
	_, out, _, err = r.Handle("/EventScheduleInfo", wire.AppendVarint(nil, 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ = wire.Bytes(out, 1)
	if active, _, _ := wire.Varint(b, 7); active != 0 {
		t.Fatal("end boundary active")
	}
	if _, err = r.Resolve(78); err == nil {
		t.Fatal("unknown uid resolved")
	}
	if err = r.Replace([]Schedule{{UID: 77, Type: 2, ID: 3, Start: 100, End: 200}}); err == nil {
		t.Fatal("unknown type accepted")
	}
	if s, _ = r.Resolve(77); s.ID != 3 {
		t.Fatal("failed replacement changed calendar")
	}
}

func TestPublicZeroUIDRowsRetainSignedWindowsAndRejectAmbiguousLookup(t *testing.T) {
	r := NewRegistry()
	r.now = func() time.Time { return time.UnixMilli(100) }
	rows := []Schedule{
		{Type: 4, ID: 1, Start: -32400000, End: 200},
		{Type: 4, ID: 2, Start: -32400000, End: 200},
		{UID: 99, Type: 4, ID: 3, Start: 300, End: 400},
	}
	if err := r.Replace(rows); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(0); err == nil {
		t.Fatal("ambiguous UID 0 lookup succeeded")
	}
	_, body, _, err := r.Handle("/EventScheduleInfo", wire.AppendVarint(nil, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := wire.Walk(body, func(f wire.Field) error {
		if f.Number != 1 {
			return nil
		}
		count++
		start, _, _ := wire.Varint(f.Value, 5)
		active, _, _ := wire.Varint(f.Value, 7)
		if count <= 2 && (int64(start) != -32400000 || active != 1) {
			t.Fatalf("public start=%d active=%d", int64(start), active)
		}
		if count == 3 && active != 0 {
			t.Fatal("future row activated")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("rows=%d", count)
	}
	if err := r.Replace(append(rows, rows[0])); err == nil {
		t.Fatal("duplicate semantic public event accepted")
	}
	if len(r.List()) != 3 {
		t.Fatal("failed replacement changed calendar")
	}
}
