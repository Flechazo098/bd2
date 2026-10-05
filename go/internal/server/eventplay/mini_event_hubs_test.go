package eventplay

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/readonly"
	"bd2server/internal/server/wire"
	"testing"
	"time"
)

func hubField(n int, v uint64) readonly.Field { return readonly.Field{Number: n, Type: 0, Varint: v} }
func miniCalendar(uid, hub, start, playEnd, end uint64, refs ...uint64) readonly.Field {
	settings := []readonly.Field{hubField(1, 5), hubField(2, 5)}
	for _, uid := range refs {
		settings = append(settings, hubField(3, uid))
	}
	return readonly.Field{Number: 1, Type: 2, Fields: []readonly.Field{hubField(1, uid), hubField(2, hub), hubField(3, start), hubField(4, playEnd), hubField(5, end), {Number: 6, Type: 2, Fields: settings}}}
}
func attachMiniCalendar(s *Service, rows ...readonly.Field) {
	s.AttachHubCalendars(&readonly.Seed{Responses: map[string]readonly.Response{"/EventHubInfo": {Fields: rows}}})
}
func miniSlot(group, id, index, typ, content, endType uint64) []byte {
	var out []byte
	for _, x := range [][2]uint64{{6, group}, {10, id}, {11, index}, {9, typ}, {7, content}, {4, endType}} {
		out = wire.AppendVarint(out, int(x[0]), x[1])
	}
	return out
}
func TestMiniHubStaticSlotAndExplicitScheduleIdentity(t *testing.T) {
	s, _ := makeService(t)
	hub := wire.AppendVarint(nil, 14, 1003)
	hub = wire.AppendVarint(hub, 13, 1)
	s.design.Tables["PackEventHubTable"] = [][]byte{hub}
	s.design.Tables["PackEventListTable"] = [][]byte{miniSlot(1003, 4, 5, 5, 17, 1), miniSlot(1003, 2, 11, 13, 3, 0)}
	registry := events.NewRegistry()
	if err := registry.Replace([]events.Schedule{
		{UID: 40, Type: 12, ID: 17, Start: 100, End: 300},
		{UID: 41, Type: 12, ID: 17, Start: 400, End: 600},
		{UID: 42, Type: 13, ID: 17, Start: 100, End: 300},
		{UID: 43, Type: 12, ID: 17, SubID: 1003, Start: 100, End: 300},
	}); err != nil {
		t.Fatal(err)
	}
	s.registry = registry
	attachMiniCalendar(s, miniCalendar(1, 1003, 100, 200, 300, 40, 41), miniCalendar(2, 1003, 400, 500, 600, 40, 41))
	for _, now := range []int64{99, 100, 200, 300, 601} {
		s.now = func() time.Time { return time.UnixMilli(now) }
		_, body, _, err := s.HandleSession("/MiniEventHubInfo", wire.AppendVarint(nil, 1, 1), "session")
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		_ = wire.Walk(body, func(f wire.Field) error {
			if f.Number != 1 {
				return nil
			}
			count++
			slot, ok, err := wire.Bytes(f.Value, 6)
			if err != nil || !ok || num(slot, 1) != 4 || num(slot, 2) != 5 || num(slot, 3) != 17 {
				t.Fatalf("bad static mapping %x", slot)
			}
			want := uint64(40)
			if num(f.Value, 1) == 2 {
				want = 41
			}
			if num(slot, 4) != want || num(slot, 6) != num(f.Value, 5) {
				t.Fatalf("bad UID/window %x", slot)
			}
			if num(f.Value, 4) == num(f.Value, 5) {
				t.Fatal("play/end window lost")
			}
			return nil
		})
		if count != 2 || len(s.state.Replies) != 0 {
			t.Fatalf("calendar count=%d cached=%d", count, len(s.state.Replies))
		}
	}
	// Same sequence must reflect an updated published calendar immediately.
	attachMiniCalendar(s, miniCalendar(2, 1003, 400, 500, 600, 41))
	_, body, _, err := s.HandleSession("/MiniEventHubInfo", wire.AppendVarint(nil, 1, 1), "session")
	if err != nil {
		t.Fatal(err)
	}
	raw, _, _ := wire.Bytes(body, 1)
	if num(raw, 1) != 2 {
		t.Fatal("stale account reply returned")
	}
}
func TestMiniHubRequiresExplicitCalendarAndRejectsAmbiguousSlot(t *testing.T) {
	s, _ := makeService(t)
	hub := wire.AppendVarint(nil, 14, 1003)
	hub = wire.AppendVarint(hub, 13, 1)
	s.design.Tables["PackEventHubTable"] = [][]byte{hub}
	s.design.Tables["PackEventListTable"] = [][]byte{miniSlot(1003, 4, 5, 5, 17, 1)}
	registry := events.NewRegistry()
	_ = registry.Replace([]events.Schedule{{UID: 1, Type: 8, ID: 2010, Start: 100, End: 300}, {UID: 40, Type: 12, ID: 17, Start: 100, End: 300}, {UID: 41, Type: 12, ID: 17, Start: 100, End: 300}})
	s.registry = registry
	_, body, _, err := s.HandleSession("/MiniEventHubInfo", wire.AppendVarint(nil, 1, 1), "session")
	if err != nil || len(body) != 0 {
		t.Fatal("hub guessed from pack schedule", err)
	}
	attachMiniCalendar(s, miniCalendar(1, 1003, 100, 200, 300, 40, 41))
	if _, _, _, err = s.HandleSession("/MiniEventHubInfo", wire.AppendVarint(nil, 1, 1), "session"); err == nil {
		t.Fatal("ambiguous slot silently selected")
	}
	if len(s.state.Replies) != 0 {
		t.Fatal("failed public query persisted")
	}
}
func TestMiniHubExplicitInvalidBindingIsRejected(t *testing.T) {
	for _, tc := range []struct {
		name               string
		typ, sub, progress uint64
	}{{"wrong event type", 13, 0, 5}, {"wrong sub id", 12, 1003, 5}, {"wrong content type", 12, 0, 3}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := makeService(t)
			hub := wire.AppendVarint(nil, 14, 1003)
			hub = wire.AppendVarint(hub, 13, 1)
			s.design.Tables["PackEventHubTable"] = [][]byte{hub}
			s.design.Tables["PackEventListTable"] = [][]byte{miniSlot(1003, 4, 5, 5, 17, 1)}
			registry := events.NewRegistry()
			_ = registry.Replace([]events.Schedule{{UID: 40, Type: tc.typ, ID: 17, SubID: tc.sub, Start: 100, End: 300}})
			s.registry = registry
			field := miniCalendar(1, 1003, 100, 200, 300, 40)
			field.Fields[5].Fields[1] = hubField(2, tc.progress)
			attachMiniCalendar(s, field)
			if _, _, _, err := s.HandleSession("/MiniEventHubInfo", wire.AppendVarint(nil, 1, 1), "session"); err == nil {
				t.Fatal("invalid explicit binding accepted")
			}
		})
	}
}
func TestNormalAndMiniHubCalendarsUseSeparatePrefabs(t *testing.T) {
	s, _ := makeService(t)
	normal := wire.AppendVarint(nil, 14, 58)
	mini := wire.AppendVarint(nil, 14, 1003)
	mini = wire.AppendVarint(mini, 13, 1)
	s.design.Tables["PackEventHubTable"] = [][]byte{normal, mini}
	// No settings here: the calendars still expose their complete windows.
	a := miniCalendar(1, 58, 100, 200, 300)
	a.Fields = a.Fields[:5]
	b := miniCalendar(2, 1003, 400, 500, 600)
	b.Fields = b.Fields[:5]
	attachMiniCalendar(s, a, b)
	for _, tc := range []struct {
		path string
		uid  uint64
	}{{"/EventHubInfo", 1}, {"/MiniEventHubInfo", 2}} {
		_, body, _, err := s.HandleSession(tc.path, wire.AppendVarint(nil, 1, 1), "session")
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		_ = wire.Walk(body, func(f wire.Field) error {
			if f.Number == 1 {
				count++
				if num(f.Value, 1) != tc.uid || num(f.Value, 4) == num(f.Value, 5) {
					t.Fatalf("bad calendar %s %x", tc.path, f.Value)
				}
			}
			return nil
		})
		if count != 1 {
			t.Fatal("hub appeared in both prefab routes", tc.path, count)
		}
	}
}
func TestMiniHubExplicitUnsupportedQuizIsRejected(t *testing.T) {
	s, _ := makeService(t)
	hub := wire.AppendVarint(nil, 14, 1003)
	hub = wire.AppendVarint(hub, 13, 1)
	s.design.Tables["PackEventHubTable"] = [][]byte{hub}
	s.design.Tables["PackEventListTable"] = [][]byte{miniSlot(1003, 2, 11, 13, 3, 0)}
	field := miniCalendar(1, 1003, 100, 200, 300, 40)
	field.Fields[5].Fields[0] = hubField(1, 11)
	field.Fields[5].Fields[1] = hubField(2, 13)
	attachMiniCalendar(s, field)
	if _, _, _, err := s.HandleSession("/MiniEventHubInfo", wire.AppendVarint(nil, 1, 1), "session"); err == nil {
		t.Fatal("unsupported quiz enabled with invented UID")
	}
}
func TestPublicHubsWithoutProjectCalendarDoNotGuessSchedules(t *testing.T) {
	s, _ := makeService(t)
	registry := events.NewRegistry()
	if err := registry.Replace([]events.Schedule{{UID: 1, Type: 8, ID: 2010, Start: 100, End: 300}, {UID: 2, Type: 11, ID: 41, Start: 100, End: 300}}); err != nil {
		t.Fatal(err)
	}
	s.registry = registry
	for _, path := range []string{"/EventHubInfo", "/MiniEventHubInfo", "/MiniGameHubInfo"} {
		code, body, handled, err := s.HandleSession(path, wire.AppendVarint(nil, 1, 1), "session")
		if err != nil || !handled || code != codes[path] || len(body) != 0 {
			t.Fatalf("path=%s code=%d handled=%t body=%x err=%v", path, code, handled, body, err)
		}
	}
	if len(s.state.Replies) != 0 {
		t.Fatal("public calendar absence persisted")
	}
}
