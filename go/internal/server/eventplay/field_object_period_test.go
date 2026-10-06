package eventplay

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"testing"
	"time"
)

func TestLostCoinPeriodFollowsCalendarIdentityPackAndWindow(t *testing.T) {
	registry := events.NewRegistry()
	row := wire.AppendVarint(wire.AppendVarint(nil, 4, 9), 1, 21)
	s := &Service{registry: registry, design: &gamedata.EventPlayCatalog{Tables: map[string][][]byte{"EventLostCoinTable": {row}}}, now: func() time.Time { return time.UnixMilli(150) }}
	if err := registry.Replace([]events.Schedule{{UID: 1, Type: 14, ID: 9, Start: 100, End: 200}}); err != nil {
		t.Fatal(err)
	}
	first, end, err := s.FieldObjectEventPeriod(21)
	if err != nil || end != 200 || first == "" {
		t.Fatal(first, end, err)
	}
	if _, _, err = s.FieldObjectEventPeriod(22); err == nil {
		t.Fatal("event applied to unlisted pack")
	}
	s.now = func() time.Time { return time.UnixMilli(200) }
	if _, _, err = s.FieldObjectEventPeriod(21); err == nil {
		t.Fatal("event remained open at end boundary")
	}
	if err = registry.Replace([]events.Schedule{{UID: 2, Type: 14, ID: 9, Start: 200, End: 300}}); err != nil {
		t.Fatal(err)
	}
	next, _, err := s.FieldObjectEventPeriod(21)
	if err != nil || first == next {
		t.Fatal("new calendar reused old claim period", err)
	}
}
