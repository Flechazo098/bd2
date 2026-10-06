package eventtasks

import (
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/world"
	"errors"
	"testing"
	"time"
)

type narrowNoticeProvider struct {
	noticeProvider
	fullCalls, narrowCalls int
}

func (p *narrowNoticeProvider) Snapshot() (world.GameplayAchievementSnapshot, error) {
	p.fullCalls++
	return p.noticeProvider.Snapshot()
}
func (p *narrowNoticeProvider) InventorySnapshot() (world.GameplayAchievementSnapshot, error) {
	p.narrowCalls++
	return p.noticeProvider.Snapshot()
}

type failedObserverStore struct{ stateio.Store }

func (s failedObserverStore) Save(string, []byte) error { return errors.New("observer save failure") }

func TestObserverUsesInventoryProjectionAndRestoresMemoryOnSaveFailure(t *testing.T) {
	s, _, store := setup(t)
	p := &narrowNoticeProvider{noticeProvider: noticeProvider{count: 1}}
	s.AttachGameplayProvider(p)
	task := s.design.Missions[10]
	task.Type = 32
	s.design.Missions[10] = task
	if e := s.BeforeDispatch("/grant", req(1)); e != nil {
		t.Fatal(e)
	}
	p.count = 2
	s.store = failedObserverStore{store}
	if _, e := s.AfterDispatch("/grant", req(1), nil); e == nil {
		t.Fatal("observer persistence failure ignored")
	}
	if len(s.state.Missions) != 0 || len(s.state.Receipts) != 0 {
		t.Fatal("failed observer save retained partial tasks or receipt")
	}
	if p.fullCalls != 0 || p.narrowCalls != 2 {
		t.Fatalf("observer requested expensive full snapshot: full=%d narrow=%d", p.fullCalls, p.narrowCalls)
	}
	s.store = store
	p.count = 1
	s.BeforeDispatch("/grant", req(1))
	p.count = 2
	b, e := s.AfterDispatch("/grant", req(1), nil)
	if e != nil || len(b) == 0 {
		t.Fatal("retry after failed observer persistence lost progress")
	}
}

type writeCountingStore struct {
	stateio.Store
	writes int
	bytes  int
}

func (s *writeCountingStore) Save(name string, b []byte) error {
	s.writes++
	s.bytes += len(b)
	return s.Store.Save(name, b)
}

func TestReadOnlyBatchHasNoObserverWritesButRealDeltaNotifiesOnce(t *testing.T) {
	s, _, store := setup(t)
	counter := &writeCountingStore{Store: store}
	s.store = counter
	p := &noticeProvider{count: 1}
	s.AttachGameplayProvider(p)
	start := time.Now()
	for seq := uint64(1); seq <= 57; seq++ {
		if e := s.BeforeDispatch("/read", req(seq)); e != nil {
			t.Fatal(e)
		}
		b, e := s.AfterDispatch("/read", req(seq), nil)
		if e != nil || len(b) > 0 {
			t.Fatalf("read-only changed missions: %x %v", b, e)
		}
	}
	if counter.writes != 0 || len(s.state.Receipts) != 0 {
		t.Fatalf("read-only 57 packets made %d writes/%d receipts", counter.writes, len(s.state.Receipts))
	}
	t.Logf("57 unchanged observer boundaries: %s, writes=%d", time.Since(start), counter.writes)
	task := s.design.Missions[10]
	task.Type = 32
	s.design.Missions[10] = task
	if e := s.BeforeDispatch("/grant", req(58)); e != nil {
		t.Fatal(e)
	}
	p.count = 2
	b, e := s.AfterDispatch("/grant", req(58), nil)
	if e != nil || len(b) == 0 || counter.writes != 1 {
		t.Fatalf("real delta not persisted/notified once: writes=%d body=%x error=%v", counter.writes, b, e)
	}
	// Even if an upstream replay temporarily exposes the same before/after
	// delta, the committed request receipt must not increment tasks twice.
	p.count = 1
	s.BeforeDispatch("/grant", req(58))
	p.count = 2
	b, e = s.AfterDispatch("/grant", req(58), nil)
	if e != nil || len(b) != 0 || counter.writes != 1 {
		t.Fatal("replay repeated mission increment or write")
	}
	if e = s.RecordEvent(999999, 0, 1, nil); e != nil || counter.writes != 1 {
		t.Fatal("irrelevant condition wrote state")
	}
	if e = s.RecordEvent(32, 0, 100, nil); e != nil {
		t.Fatal(e)
	}
	writes := counter.writes
	if e = s.RecordEvent(32, 0, 100, nil); e != nil || counter.writes != writes {
		t.Fatal("capped task still wrote whole snapshot")
	}
}

func BenchmarkUnchanged57PacketObserverBatch(b *testing.B) {
	// Snapshot costs belong to the provider; this benchmark isolates event
	// mission observation and persistence decisions without a user's database.
	s, _, _ := setup(b)
	p := &noticeProvider{count: 1}
	s.AttachGameplayProvider(p)
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		for seq := uint64(1); seq <= 57; seq++ {
			if e := s.BeforeDispatch("/read", req(seq)); e != nil {
				b.Fatal(e)
			}
			if _, e := s.AfterDispatch("/read", req(seq), nil); e != nil {
				b.Fatal(e)
			}
		}
	}
}
