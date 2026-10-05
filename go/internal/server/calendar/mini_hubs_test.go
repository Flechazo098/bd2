package calendar

import (
	"testing"
	"time"

	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
)

func TestMiniHubPublishedRoutesValidateIndependentEnumsAndWindows(t *testing.T) {
	row := func(fields ...uint64) []byte {
		var b []byte
		for i := 0; i < len(fields); i += 2 {
			b = wire.AppendVarint(b, int(fields[i]), fields[i+1])
		}
		return b
	}
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	format := func(d time.Duration) string { return start.Add(d).Format(time.RFC3339) }
	design := &gamedata.EventPlayCatalog{Tables: map[string][][]byte{
		"PackEventHubTable":  {row(14, 7, 13, 1)},
		"PackEventListTable": {row(6, 7, 10, 42, 11, 5, 9, 12, 7, 99, 4, 1)},
	}}
	fresh := func() *Set {
		return &Set{
			Events:    []events.Schedule{{UID: 21, Type: 19, ID: 99, Start: start.UnixMilli(), End: start.Add(48 * time.Hour).UnixMilli()}},
			EventHubs: []EventHub{{UID: 1, HubID: 7, Start: format(0), PlayEnd: format(24 * time.Hour), End: format(48 * time.Hour), Settings: []HubSetting{{Slot: 5, ProgressType: 12, EventUIDs: []uint64{21}}}}},
		}
	}
	if err := fresh().validateMiniHubBindings(design); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*Set)
	}{
		{"wrong content enum", func(s *Set) { s.EventHubs[0].Settings[0].ProgressType = 19 }},
		{"table id is not slot index", func(s *Set) { s.EventHubs[0].Settings[0].Slot = 42 }},
		{"missing UID", func(s *Set) { s.EventHubs[0].Settings[0].EventUIDs = []uint64{22} }},
		{"wrong event enum", func(s *Set) { s.Events[0].Type = 12 }},
		{"wrong design id", func(s *Set) { s.Events[0].ID = 7 }},
		{"wrong sub identity", func(s *Set) { s.Events[0].SubID = 7 }},
		{"no window overlap", func(s *Set) {
			s.Events[0].Start = start.Add(72 * time.Hour).UnixMilli()
			s.Events[0].End = start.Add(96 * time.Hour).UnixMilli()
		}},
		{"ambiguous schedule", func(s *Set) {
			other := s.Events[0]
			other.UID = 22
			s.Events = append(s.Events, other)
			s.EventHubs[0].Settings[0].EventUIDs = append(s.EventHubs[0].Settings[0].EventUIDs, 22)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := fresh()
			test.change(s)
			if err := s.validateMiniHubBindings(design); err == nil {
				t.Fatal("invalid published route accepted")
			}
		})
	}
	// A future hub must validate without comparing its dates to the current clock.
	design.Tables["PackEventListTable"][0] = row(6, 7, 10, 42, 11, 5, 9, 13, 7, 99, 4, 1)
	s := fresh()
	s.EventHubs[0].Settings[0].ProgressType = 13
	if err := s.validateMiniHubBindings(design); err == nil {
		t.Fatal("quiz borrowed global bingo event type")
	}
	design.Tables["PackEventListTable"][0] = row(6, 7, 10, 42, 11, 11, 9, 13, 7, 99)
	s = fresh()
	s.EventHubs[0].Settings = []HubSetting{{Slot: 11, ProgressType: 13, EventUIDs: []uint64{10000032}}}
	if err := s.validateMiniHubBindings(design); err != nil {
		t.Fatalf("independent quiz content UID rejected: %v", err)
	}
	s.EventHubs[0].Settings[0].EventUIDs = []uint64{21}
	if err := s.validateMiniHubBindings(design); err == nil {
		t.Fatal("quiz content UID collided with global event")
	}
}
