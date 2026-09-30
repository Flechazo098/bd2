package schedule

import (
	"testing"

	"bd2server/internal/server/wire"
)

func TestVersion23510ContainsRequiredContentSix(t *testing.T) {
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

func TestScheduleRejectsMissingSequence(t *testing.T) {
	if _, _, handled, err := Current().Handle("/ScheduleInfo", nil); !handled || err == nil {
		t.Fatalf("missing sequence handled=%v err=%v", handled, err)
	}
}
