package schedule

import (
	"os"
	"path/filepath"
	"testing"

	"bd2server/internal/server/wire"
)

func TestCalendarReloadsChangedIDsAndTimesFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	for _, revision := range []struct {
		id, start, end uint64
		raw            string
	}{
		{709, 101, 303, `{"version":"2.35.10","calculate_milliseconds":17,"contents":[{"id":81,"current":{"id":709,"start_milliseconds":101,"end_milliseconds":303},"next":{"id":710,"start_milliseconds":401,"end_milliseconds":501}}],"regular":[{"content_id":81,"season":9}]}`},
		{811, 701, 903, `{"version":"2.35.10","calculate_milliseconds":17,"contents":[{"id":81,"current":{"id":811,"start_milliseconds":701,"end_milliseconds":903},"next":{"id":812,"start_milliseconds":1001,"end_milliseconds":1201}}],"regular":[{"content_id":81,"season":11}]}`},
	} {
		if err := os.WriteFile(path, []byte(revision.raw), 0600); err != nil {
			t.Fatal(err)
		}
		service, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		_, response, _, err := service.Handle("/ScheduleInfo", wire.AppendVarint(nil, 1, 1))
		if err != nil {
			t.Fatal(err)
		}
		content, _, _ := wire.Bytes(response, 2)
		current, _, _ := wire.Bytes(content, 2)
		id, _, _ := wire.Varint(current, 1)
		start, _, _ := wire.Varint(current, 2)
		end, _, _ := wire.Varint(current, 3)
		if id != revision.id || start != revision.start || end != revision.end {
			t.Fatalf("season=%d times=%d/%d", id, start, end)
		}
	}
	if err := os.WriteFile(path, []byte(`{"version":"2.35.10","calculate_milliseconds":1,"contents":[{"id":1,"current":{"id":2,"start_milliseconds":4,"end_milliseconds":3}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("reversed date accepted")
	}
}

func TestVersion23510ContainsRequiredContentSix(t *testing.T) {
	service, err := Load("../../../seed/v2_35_10/schedule.json")
	if err != nil {
		t.Fatal(err)
	}
	code, response, handled, err := service.Handle("/ScheduleInfo", wire.AppendVarint(nil, 1, 1))
	if err != nil || !handled || code != 117 {
		t.Fatalf("schedule code=%d handled=%v err=%v", code, handled, err)
	}
	found := false
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number == 2 {
			id, _, _ := wire.Varint(field.Value, 1)
			if id == 6 {
				found = true
				current, ok, _ := wire.Bytes(field.Value, 2)
				season, _, _ := wire.Varint(current, 1)
				if !ok || season != 26 {
					t.Fatalf("content 6 current season=%d found=%v", season, ok)
				}
			}
		}
		return nil
	}); err != nil || !found {
		t.Fatalf("content 6 missing err=%v", err)
	}
}

func TestScheduleRejectsMissingSequence(t *testing.T) {
	if _, _, handled, err := (&Service{}).Handle("/ScheduleInfo", nil); !handled || err == nil {
		t.Fatalf("missing sequence handled=%v err=%v", handled, err)
	}
}
