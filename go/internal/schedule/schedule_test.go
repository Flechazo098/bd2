package schedule

import (
	"bytes"
	"os"
	"testing"

	"bd2server/internal/wire"
)

func TestVersion23413ContainsRequiredContentSix(t *testing.T) {
	service := Current()
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

func TestVersion23413MatchesOptionalOfficialResponse(t *testing.T) {
	path := os.Getenv("BD2_TEST_SCHEDULE_CAPTURE")
	if path == "" {
		t.Skip("set BD2_TEST_SCHEDULE_CAPTURE to compare an official response")
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("optional official schedule sample unavailable: %v", err)
	}
	_, got, _, err := Current().Handle("/ScheduleInfo", wire.AppendVarint(nil, 1, 2))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("schedule differs from official 2.34.13 response: equal=%v err=%v\ngot=%x\nwant=%x", bytes.Equal(got, want), err, got, want)
	}
}

func TestScheduleRejectsMissingSequence(t *testing.T) {
	if _, _, handled, err := Current().Handle("/ScheduleInfo", nil); !handled || err == nil {
		t.Fatalf("missing sequence handled=%v err=%v", handled, err)
	}
}
