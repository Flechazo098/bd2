package world

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"testing"
)

type achievementQueryStore struct {
	*accountstate.Repository
	lists, loads, saves int
}

func (s *achievementQueryStore) ListEntries(domain, bucket string) (map[string][]byte, error) {
	s.lists++
	return s.Repository.ListEntries(domain, bucket)
}
func (s *achievementQueryStore) LoadEntry(domain, bucket, key string) ([]byte, bool, error) {
	s.loads++
	return s.Repository.LoadEntry(domain, bucket, key)
}
func (s *achievementQueryStore) SaveWithEntries(domain string, core []byte, entries []stateio.EntryMutation) error {
	s.saves++
	return s.Repository.SaveWithEntries(domain, core, entries)
}

func TestAchievementProgressBoundaryIsAtomicOrderedAndReplayable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	store := &achievementQueryStore{Repository: repo}
	design := &gamedata.AchievementCounterDesign{Groups: map[int][]int{1: {0}, 2: {0}}, Conditions: map[int]gamedata.AchievementCondition{1: {Type: 14, SubType: 21}, 2: {Type: 7}}}
	s, err := NewAchievementService(design, store)
	if err != nil {
		t.Fatal(err)
	}
	s.BeginSession("test")
	conditions := []GameplayAchievementCondition{{Type: 14, SubType: 21, Value: 1}}
	events := []GameplayAchievementRecordedEvent{{Identity: "a", Type: 14, SubType: 21, Count: 3}, {Identity: "b", Type: 14, SubType: 21, Count: 4}, {Identity: "b", Type: 14, SubType: 21, Count: 4}}
	before, after, err := s.ApplyGameplayProgress(conditions, events)
	if err != nil || len(before) != 0 || !reflect.DeepEqual(after, map[int]uint64{1: 8}) {
		t.Fatalf("ordered boundary before=%v after=%v err=%v", before, after, err)
	}
	if store.lists != 1 || store.saves != 1 {
		t.Fatalf("boundary lists=%d saves=%d", store.lists, store.saves)
	}
	if _, after, err = s.ApplyGameplayProgress(nil, events); err != nil || after[1] != 8 {
		t.Fatalf("receipt replay after=%v err=%v", after, err)
	}
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	// A conflicting event after a new event must install neither event nor
	// the preceding condition changes, even before outer rollback executes.
	bad := append([]GameplayAchievementRecordedEvent{{Identity: "unwritten", Type: 7, Count: 5}}, GameplayAchievementRecordedEvent{Identity: "a", Type: 14, SubType: 21, Count: 9})
	if _, _, err := s.ApplyGameplayProgress([]GameplayAchievementCondition{{Type: 7, Value: 2}}, bad); err == nil {
		t.Fatal("receipt conflict accepted")
	}
	if err := op.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repo.LoadEntry("missions", "achievement_events", "unwritten"); err != nil || found {
		t.Fatalf("failed boundary persisted event=%v err=%v", found, err)
	}
	if got, err := s.CounterValues(); err != nil || !reflect.DeepEqual(got, map[int]uint64{1: 8}) {
		t.Fatalf("failed boundary installed partial counts=%v err=%v", got, err)
	}
	if _, _, err := s.ApplyGameplayProgress([]GameplayAchievementCondition{{Type: 7, Value: math.MaxInt64}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ApplyGameplayProgress(conditions, []GameplayAchievementRecordedEvent{{Identity: "overflow", Type: 7, Count: 1}}); err == nil {
		t.Fatal("overflow accepted")
	}
	if value, err := s.AchievementValue(1); err != nil || value != 8 {
		t.Fatalf("overflow installed preceding condition=%d err=%v", value, err)
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ApplyGameplayProgress(nil, []GameplayAchievementRecordedEvent{{Identity: "rolled-back", Type: 14, SubType: 21, Count: 2}}); err != nil {
		t.Fatal(err)
	}
	if err := op.Rollback(); !errors.Is(err, stateio.ErrStateRecoveryRequired) {
		t.Fatalf("dirty rollback=%v", err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Repository = repo
	s, err = NewAchievementService(design, store)
	if err != nil {
		t.Fatal(err)
	}
	_, after, err = s.ApplyGameplayProgress(nil, []GameplayAchievementRecordedEvent{{Identity: "rolled-back", Type: 14, SubType: 21, Count: 2}})
	if err != nil || after[1] != 10 {
		t.Fatalf("rollback poisoned retry counts=%v err=%v", after, err)
	}
}

// This comparison uses the prior public counter operations as the baseline,
// and the optimized boundary against the same synthetic SQLite counter set.
// The 57-read case models login: no events, with 20 completed pack conditions.
func BenchmarkAchievementProgressBoundary(b *testing.B) {
	for _, eventCount := range []int{0, 20} {
		for _, bulk := range []bool{false, true} {
			name := fmt.Sprintf("events%d/bulk%v", eventCount, bulk)
			b.Run(name, func(b *testing.B) {
				repo, err := accountstate.Open(filepath.Join(b.TempDir(), "state.db"))
				if err != nil {
					b.Fatal(err)
				}
				defer repo.Close()
				store := &achievementQueryStore{Repository: repo}
				design := &gamedata.AchievementCounterDesign{Groups: map[int][]int{}, Conditions: map[int]gamedata.AchievementCondition{}}
				for id := 1; id <= 500; id++ {
					design.Groups[id] = []int{0}
					design.Conditions[id] = gamedata.AchievementCondition{Type: 14, SubType: uint64(id)}
				}
				s, err := NewAchievementService(design, store)
				if err != nil {
					b.Fatal(err)
				}
				s.BeginSession("benchmark")
				var conditions []GameplayAchievementCondition
				for id := 1; id <= 20; id++ {
					conditions = append(conditions, GameplayAchievementCondition{Type: 14, SubType: uint64(id), Value: 1})
				}
				var seed []stateio.EntryMutation
				for id := 1; id <= 500; id++ {
					seed = append(seed, stateio.EntryMutation{Bucket: "achievement_counts", Key: fmt.Sprint(id), Payload: []byte("1")})
				}
				if err := repo.SaveWithEntries("missions", nil, seed); err != nil {
					b.Fatal(err)
				}
				store.lists, store.loads, store.saves = 0, 0, 0
				b.ReportAllocs()
				b.ResetTimer()
				for iteration := 0; iteration < b.N; iteration++ {
					op, err := repo.BeginOperation()
					if err != nil {
						b.Fatal(err)
					}
					for request := 0; request < 57; request++ {
						if bulk {
							_, _, err = s.ApplyGameplayProgress(conditions, nil)
						} else {
							_, err = s.CounterValues()
							for _, c := range conditions {
								if err == nil {
									_, err = s.SetCondition(c.Type, c.SubType, c.Value)
								}
							}
						}
						if err != nil {
							b.Fatal(err)
						}
						var events []GameplayAchievementRecordedEvent
						for id := 0; id < eventCount; id++ {
							events = append(events, GameplayAchievementRecordedEvent{Identity: fmt.Sprintf("%d/%d/%d", iteration, request, id), Type: 14, SubType: uint64(id + 1), Count: 1})
						}
						if bulk {
							_, _, err = s.ApplyGameplayProgress(conditions, events)
						} else {
							for _, c := range conditions {
								if err == nil {
									_, err = s.SetCondition(c.Type, c.SubType, c.Value)
								}
							}
							for _, e := range events {
								if err == nil {
									_, err = s.RecordEvent(e.Identity, e.Type, e.SubType, e.Count)
								}
							}
							if err == nil {
								_, err = s.CounterValues()
							}
						}
						if err != nil {
							b.Fatal(err)
						}
					}
					if err := op.Commit(); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(store.lists)/float64(b.N), "bucket-reads/op")
				b.ReportMetric(float64(store.loads)/float64(b.N), "entry-reads/op")
				b.ReportMetric(float64(store.saves)/float64(b.N), "entry-writes/op")
			})
		}
	}
}
