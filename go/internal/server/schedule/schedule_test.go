package schedule

import (
	"encoding/json"
	"testing"

	"bd2server/internal/server/wire"
)

func TestCalendarUsesInjectedIDsAndTimes(t *testing.T) {
	for _, revision := range []struct {
		id, start, end uint64
		raw            string
	}{
		{709, 101, 303, `{"version":"2.35.10","calculate_milliseconds":17,"contents":[{"id":81,"current":{"id":709,"start_milliseconds":101,"end_milliseconds":303},"next":{"id":710,"start_milliseconds":401,"end_milliseconds":501}}],"regular":[{"content_id":81,"season":9}]}`},
		{811, 701, 903, `{"version":"2.35.10","calculate_milliseconds":17,"contents":[{"id":81,"current":{"id":811,"start_milliseconds":701,"end_milliseconds":903},"next":{"id":812,"start_milliseconds":1001,"end_milliseconds":1201}}],"regular":[{"content_id":81,"season":11}]}`},
	} {
		var service Service
		if err := json.Unmarshal([]byte(revision.raw), &service); err != nil {
			t.Fatal(err)
		}
		if err := service.Validate(); err != nil {
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
}

func TestScheduleRejectsMissingSequence(t *testing.T) {
	if _, _, handled, err := (&Service{}).Handle("/ScheduleInfo", nil); !handled || err == nil {
		t.Fatalf("missing sequence handled=%v err=%v", handled, err)
	}
}
