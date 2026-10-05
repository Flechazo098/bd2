package eventplay

import (
	"bytes"
	"testing"

	"bd2server/internal/server/readonly"
	"bd2server/internal/server/wire"
)

func TestProjectHubCalendarRetainsPlayEndAndMultipleUIDs(t *testing.T) {
	s, _ := makeService(t)
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
		"/EventHubInfo": {Fields: []readonly.Field{hub}},
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
