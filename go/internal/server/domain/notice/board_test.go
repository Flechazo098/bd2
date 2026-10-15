package notice

import (
	"testing"
	"time"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func notice(id int32, title string, start time.Time) Notice {
	return Notice{ID: id, Type: TypeUpdate, Start: start, End: start.Add(24 * time.Hour), Text: map[string]Content{"en_us": {Title: title, Blocks: []Block{{Value: title}}}}}
}

func ids(notices []Notice) map[int32]bool {
	out := map[int32]bool{}
	for _, n := range notices {
		out[n.ID] = true
	}
	return out
}

// A client that already holds the board's sequence must receive only what
// changed afterwards, because it keeps every notice it was sent before.
func TestBoardSendsOnlyChangesAfterTheClientsSequence(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	board := NewBoard(func() time.Time { return now })
	board.Replace(SourceChangelog, []Notice{notice(1, "a", now.Add(-time.Hour)), notice(2, "b", now.Add(-time.Hour))})
	seen := board.Sequence()
	if got := ids(board.Since(0)); !got[1] || !got[2] || len(got) != 2 {
		t.Fatalf("full list = %v", got)
	}

	board.Replace(SourceChangelog, []Notice{notice(1, "a", now.Add(-time.Hour)), notice(2, "b", now.Add(-time.Hour))})
	if board.Sequence() != seen {
		t.Fatal("republishing identical notices advanced the sequence, so every client would refetch")
	}

	board.Replace(SourceChangelog, []Notice{notice(1, "a", now.Add(-time.Hour)), notice(2, "b edited", now.Add(-time.Hour))})
	board.Replace("maintenance", []Notice{notice(3, "m", now)})
	if board.Sequence() <= seen {
		t.Fatal("a change did not advance the sequence")
	}
	if got := ids(board.Since(seen)); !got[2] || !got[3] || got[1] || len(got) != 2 {
		t.Fatalf("incremental list after %d = %v; want the edited and the new notice only", seen, got)
	}
	if got := ids(board.Since(0)); len(got) != 3 {
		t.Fatalf("a maintenance replacement removed changelog notices: %v", got)
	}
}

func TestBoardHidesNoticesOutsideTheirWindow(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	board := NewBoard(func() time.Time { return now })
	expired := notice(1, "old", now.Add(-48*time.Hour))
	future := notice(2, "later", now.Add(time.Hour))
	current := notice(3, "now", now.Add(-time.Minute))
	board.Replace(SourceChangelog, []Notice{expired, future, current})
	if got := ids(board.Since(0)); len(got) != 1 || !got[3] {
		t.Fatalf("visible notices = %v; want only the current one", got)
	}
}

func TestNoticeInfoEncodesMillisecondTimesAndContentBlocks(t *testing.T) {
	start := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	board := NewBoard(func() time.Time { return start.Add(time.Hour) })
	board.Replace(SourceChangelog, []Notice{{ID: 7, Type: TypeEmergency, Start: start, End: start.Add(time.Hour * 2), Pinned: true,
		Text: map[string]Content{"en_us": {Title: "Maintenance", Blocks: []Block{{Value: "text"}, {Image: true, Value: "https://example.com/a.png"}}}}}})
	body, err := board.NoticeInfo(nil, "en_us")
	if err != nil {
		t.Fatal(err)
	}
	info, ok, err := wire.Bytes(body, 1)
	if err != nil || !ok {
		t.Fatalf("notice_info missing: %v", err)
	}
	for field, want := range map[int]uint64{1: 7, 2: uint64(TypeEmergency), 5: uint64(start.UnixMilli()), 6: uint64(start.Add(2 * time.Hour).UnixMilli()), 10: 1} {
		got, _, err := wire.Varint(info, field)
		if err != nil || got != want {
			t.Errorf("NoticeDBInfo field %d = %d, %v; want %d", field, got, err, want)
		}
	}
	var contents [][]byte
	err = wire.Walk(info, func(f wire.Field) error {
		if f.Number == 8 {
			contents = append(contents, f.Value)
		}
		return nil
	})
	if err != nil || len(contents) != 2 {
		t.Fatalf("content blocks = %d, %v; want 2", len(contents), err)
	}
	for i, want := range []struct {
		kind  uint64
		value string
	}{{0, "text"}, {1, "https://example.com/a.png"}} {
		kind, _, _ := wire.Varint(contents[i], 1)
		value, _, _ := wire.Bytes(contents[i], 2)
		if kind != want.kind || string(value) != want.value {
			t.Errorf("block %d = type %d %q; want type %d %q", i, kind, value, want.kind, want.value)
		}
	}
}

// Players read notices in the language they logged in with; a language the
// notice was not written in falls back to English.
func TestNoticeInfoAnswersInTheAccountsLoginLanguage(t *testing.T) {
	start := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	board := NewBoard(func() time.Time { return start.Add(time.Hour) })
	board.Replace(SourceChangelog, []Notice{{ID: 1, Start: start, End: start.Add(48 * time.Hour), Text: map[string]Content{
		"en_us": {Title: "Update"}, "zh_cn": {Title: "更新"},
	}}})
	board.SetLanguage("chinese-player", "zh_cn")
	board.SetLanguage("korean-player", "ko_kr")
	for account, want := range map[string]string{"chinese-player": "更新", "korean-player": "Update", "never-logged-in": "Update"} {
		_, body, handled, err := Handler{Board: board}.Handle(command.Context{Identity: command.Identity{AccountID: account}}, "/NoticeInfo", nil)
		if err != nil || !handled {
			t.Fatalf("%s: handled=%v err=%v", account, handled, err)
		}
		info, _, _ := wire.Bytes(body, 1)
		title, _, _ := wire.Bytes(info, 3)
		if string(title) != want {
			t.Errorf("%s sees title %q, want %q", account, title, want)
		}
	}
}
