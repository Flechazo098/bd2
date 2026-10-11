package ranking

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"bd2server/internal/server/storage/shared"
)

func teamTestStore(t *testing.T) Store {
	t.Helper()
	store, err := shared.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return testPVPStore{store}
}
func testTeamRun(id string, milliseconds int64, members ...TeamMember) CompletedTeamRun {
	return CompletedTeamRun{ResultID: id, InstanceID: "room-" + id, EventUID: 7, Monster: 8, StageType: 2, Clear: true, Milliseconds: milliseconds, Members: members}
}
func TestTeamRankingsKeepFastestCanonicalTeamAndOwnRecord(t *testing.T) {
	ctx := context.Background()
	store := teamTestStore(t)
	teams, err := NewTeams(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	alice := TeamMember{AccountID: "alice", OwnerIndex: 11, Name: "Alice", CharacterID: 1}
	bob := TeamMember{AccountID: "bob", OwnerIndex: 22, Name: "Bob", CharacterID: 2}
	carol := TeamMember{AccountID: "carol", OwnerIndex: 33, Name: "Carol", CharacterID: 3}
	for _, run := range []CompletedTeamRun{testTeamRun("first", 40000, bob, alice), testTeamRun("slower", 50000, alice, bob), testTeamRun("faster", 35000, bob, alice), testTeamRun("leaders", 30000, carol, alice)} {
		if err := teams.Record(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	solo := testTeamRun("solo", 25000, alice)
	solo.StageType = 1
	if err := teams.Record(ctx, solo); err != nil {
		t.Fatal(err)
	}
	reversed := testTeamRun("faster", 35000, alice, bob)
	if err := teams.Record(ctx, reversed); err != nil {
		t.Fatal("member order changed committed identity:", err)
	}
	conflicting := reversed
	conflicting.Milliseconds = 34000
	if err := teams.Record(ctx, conflicting); err == nil {
		t.Fatal("changed outcome reused completed run identity")
	}
	teams, err = NewTeams(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := teams.Rankings(TeamQuery{OwnerID: "bob", EventUID: 7, Monster: 8, StageType: 2, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Rank != 1 || rows[0].Milliseconds != 30000 || rows[0].Own || rows[1].Rank != 2 || rows[1].Milliseconds != 35000 || !rows[1].Own || rows[1].TopPercent != 100 {
		t.Fatalf("wrong fastest and own records after reload: %+v", rows)
	}
	if got := []uint64{rows[1].Members[0].OwnerIndex, rows[1].Members[1].OwnerIndex}; !slices.Equal(got, []uint64{11, 22}) {
		t.Fatalf("team identity not canonical: %v", got)
	}
	rows[1].Members[0].Name = "mutated"
	rows, err = teams.Rankings(TeamQuery{OwnerID: "alice", EventUID: 7, Monster: 8, StageType: 2, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || !rows[0].Own || rows[1].Own || rows[1].Members[0].Name != "Alice" {
		t.Fatalf("own best selection or snapshot isolation broken: %+v", rows)
	}
	soloRows, err := teams.Rankings(TeamQuery{OwnerID: "alice", EventUID: 7, Monster: 8, StageType: 1, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(soloRows) != 1 || soloRows[0].Milliseconds != 25000 || soloRows[0].Rank != 1 {
		t.Fatalf("solo and party were merged: %+v", soloRows)
	}
}

type teamFailingStore struct {
	Store
	fail bool
}

func (s *teamFailingStore) Update(ctx context.Context, write func(Tx) error) error {
	return s.Store.Update(ctx, func(tx Tx) error {
		if err := write(tx); err != nil {
			return err
		}
		if s.fail {
			return errors.New("team transaction rolled back")
		}
		return nil
	})
}
func TestTeamRecordPublishesOnlyCommittedClear(t *testing.T) {
	ctx := context.Background()
	store := &teamFailingStore{Store: teamTestStore(t)}
	teams, err := NewTeams(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	member := TeamMember{AccountID: "a", OwnerIndex: 1, Name: "A", CharacterID: 1}
	run := testTeamRun("failed", 0, member)
	run.Clear = false
	if err = teams.Record(ctx, run); err != nil {
		t.Fatal(err)
	}
	rows, err := teams.Rankings(TeamQuery{Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("failed encounter entered ranking: %v %+v", err, rows)
	}
	invalid := testTeamRun("zero-clear", 0, member)
	if err = teams.Record(ctx, invalid); err == nil {
		t.Fatal("nonpositive clear time accepted")
	}
	run = testTeamRun("success", 12345, member)
	store.fail = true
	if err = teams.Record(ctx, run); err == nil {
		t.Fatal("expected storage failure")
	}
	rows, err = teams.Rankings(TeamQuery{Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("uncommitted clear entered in-memory board: %v %+v", err, rows)
	}
	store.fail = false
	teams, err = NewTeams(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if err = teams.Record(ctx, run); err != nil {
		t.Fatal("rolled-back receipt prevented retry:", err)
	}
	rows, err = teams.Rankings(TeamQuery{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Milliseconds != 12345 {
		t.Fatalf("clear time did not survive commit: %v %+v", err, rows)
	}
}

type teamBlockingStore struct {
	Store
	enter, release chan struct{}
}

func (s teamBlockingStore) Update(ctx context.Context, write func(Tx) error) error {
	close(s.enter)
	select {
	case <-s.release:
		return s.Store.Update(ctx, write)
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestTeamRankingReadsDoNotWaitForStorage(t *testing.T) {
	store := teamBlockingStore{Store: teamTestStore(t), enter: make(chan struct{}), release: make(chan struct{})}
	teams, err := NewTeams(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- teams.Record(ctx, testTeamRun("in-flight", 1000, TeamMember{AccountID: "a", OwnerIndex: 1, Name: "A", CharacterID: 1}))
	}()
	select {
	case <-store.enter:
	case <-ctx.Done():
		t.Fatal("record did not reach storage")
	}
	defer func() {
		close(store.release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	read := make(chan error, 1)
	go func() {
		rows, err := teams.Rankings(TeamQuery{Limit: 10})
		if err == nil && len(rows) != 0 {
			err = errors.New("uncommitted team visible")
		}
		read <- err
	}()
	select {
	case err := <-read:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("ranking read blocked behind storage")
	}
}
