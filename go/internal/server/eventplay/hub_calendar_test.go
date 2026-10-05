package eventplay

import (
	"bd2server/internal/server/events"
	"bytes"
	"testing"

	"bd2server/internal/server/readonly"
	"bd2server/internal/server/wire"
)

func TestProjectHubCalendarRetainsPlayEndAndMultipleUIDs(t *testing.T) {
	s, _ := makeService(t)
	s.design.Tables["PackEventHubTable"] = [][]byte{wire.AppendVarint(nil, 14, 57)}
	scalar := func(n int, v uint64) readonly.Field {
		return readonly.Field{Number: n, Type: 0, Varint: v}
	}
	hub := readonly.Field{Number: 1, Type: 2, Fields: []readonly.Field{
		scalar(1, 74), scalar(2, 57), scalar(3, 100), scalar(4, 200), scalar(5, 300),
		{Number: 6, Type: 2, Fields: []readonly.Field{
			scalar(1, 1), scalar(2, 3), scalar(3, 77), scalar(3, 78),
		}},
	}}
	seed := &readonly.Seed{Responses: map[string]readonly.Response{
		"/EventHubInfo": {PacketCode: 222, Fields: []readonly.Field{hub}},
		"/MiniGameHubInfo": {PacketCode: 390, Fields: []readonly.Field{
			{Number: 1, Type: 2, Fields: []readonly.Field{scalar(1, 4), scalar(2, 9), scalar(3, 0)}},
		}},
	}}
	s.AttachHubCalendars(seed)
	for _, path := range []string{"/EventHubInfo", "/MiniGameHubInfo"} {
		req := wire.AppendVarint(nil, 1, 1)
		wantCode, want, _, err := seed.Handle(path, req)
		if err != nil {
			t.Fatal(err)
		}
		code, got, handled, err := s.HandleSession(path, req, "session")
		if err != nil || !handled || code != wantCode || !bytes.Equal(got, want) {
			t.Fatalf("path=%s code=%d handled=%v err=%v body=%x want=%x", path, code, handled, err, got, want)
		}
		if path == "/EventHubInfo" {
			raw, _, _ := wire.Bytes(got, 1)
			playEnd, _, _ := wire.Varint(raw, 4)
			end, _, _ := wire.Varint(raw, 5)
			setting, _, _ := wire.Bytes(raw, 6)
			ids, err := list(setting, 3)
			if err != nil || playEnd != 200 || end != 300 || len(ids) != 2 || ids[0] != 77 || ids[1] != 78 {
				t.Fatalf("hub play_end=%d end=%d refs=%v err=%v", playEnd, end, ids, err)
			}
		}
	}
	if len(s.state.Replies) != 0 {
		t.Fatal("public calendars persisted as player replies")
	}
}

func TestMiniEventHubExcludesOrdinaryPacksAndGameSchedules(t *testing.T) {
	s, _ := makeService(t)
	registry := events.NewRegistry()
	if err := registry.Replace([]events.Schedule{
		{UID: 1, Type: 8, ID: 20, Start: 1, End: 9999999},
		{UID: 2, Type: 11, ID: 41, Start: 1, End: 9999999},
		{UID: 3, Type: 8, ID: 1003, Start: 1, End: 9999999},
	}); err != nil {
		t.Fatal(err)
	}
	s.registry = registry
	normal := wire.AppendVarint(nil, 14, 20)
	normal = wire.AppendVarint(normal, 20, 20)
	mini := wire.AppendVarint(nil, 14, 1003)
	mini = wire.AppendVarint(mini, 20, 1003)
	mini = wire.AppendVarint(mini, 13, 1)
	s.design.Tables["PackEventHubTable"] = [][]byte{normal, mini}
	s.AttachHubCalendars(&readonly.Seed{Responses: map[string]readonly.Response{"/EventHubInfo": {Fields: []readonly.Field{
		{Number: 1, Type: 2, Fields: []readonly.Field{{Number: 1, Type: 0, Varint: 1}, {Number: 2, Type: 0, Varint: 20}, {Number: 3, Type: 0, Varint: 1}, {Number: 4, Type: 0, Varint: 9999999}, {Number: 5, Type: 0, Varint: 9999999}}},
		{Number: 1, Type: 2, Fields: []readonly.Field{{Number: 1, Type: 0, Varint: 3}, {Number: 2, Type: 0, Varint: 1003}, {Number: 3, Type: 0, Varint: 1}, {Number: 4, Type: 0, Varint: 9999999}, {Number: 5, Type: 0, Varint: 9999999}}},
	}}}})

	code, response, handled, err := s.HandleSession("/MiniEventHubInfo", wire.AppendVarint(nil, 1, 1), "session")
	if err != nil || !handled || code != 534 {
		t.Fatalf("mini hub response code=%d handled=%t err=%v", code, handled, err)
	}
	count := 0
	if err := wire.Walk(response, func(f wire.Field) error {
		if f.Number == 1 {
			count++
			if num(f.Value, 1) != 3 || num(f.Value, 2) != 1003 {
				t.Fatalf("ordinary pack or game exposed as MiniEventMainUI: %x", f.Value)
			}
		}
		return nil
	}); err != nil || count != 1 {
		t.Fatalf("mini hubs=%d err=%v", count, err)
	}
}
