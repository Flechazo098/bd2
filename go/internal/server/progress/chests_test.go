package progress

import (
	"bd2server/internal/server/stateio"
	"reflect"
	"testing"
)

func TestOpenedFieldRewardPeriodsValidatesAndOwnsSnapshot(t *testing.T) {
	storage := stateio.NewMemory()
	s, err := OpenStore(storage)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		pack, id int
		period   string
	}{{21, 71, "once"}, {22, 71, "2026-10-06"}, {22, 72, "event:777"}} {
		if err := s.MarkFieldRewardOpened(row.pack, row.id, row.period); err != nil {
			t.Fatal(err)
		}
	}
	want := map[int]map[int]string{21: {71: "once"}, 22: {71: "2026-10-06", 72: "event:777"}}
	got, err := s.OpenedFieldRewardPeriods()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("opened periods=%v err=%v", got, err)
	}
	got[21][71] = "modified"
	fresh, err := s.OpenedFieldRewardPeriods()
	if err != nil || !reflect.DeepEqual(fresh, want) {
		t.Fatalf("caller modified persisted snapshot: %v err=%v", fresh, err)
	}
	for _, row := range []struct {
		key string
		raw []byte
	}{
		{"0:71", []byte("once")}, {"21:0", []byte("once")}, {"21:71:1", []byte("once")},
		{"pack:71", []byte("once")}, {"21:object", []byte("once")}, {"021:71", []byte("once")},
		{"21:071", []byte("once")}, {"21:73", nil},
	} {
		t.Run(row.key, func(t *testing.T) {
			if err := storage.PutEntry("progress", "field_rewards", row.key, row.raw); err != nil {
				t.Fatal(err)
			}
			if _, err := s.OpenedFieldRewardPeriods(); err == nil {
				t.Fatal("malformed field reward entry accepted")
			}
			if _, err := storage.DeleteEntry("progress", "field_rewards", row.key); err != nil {
				t.Fatal(err)
			}
		})
	}
}
