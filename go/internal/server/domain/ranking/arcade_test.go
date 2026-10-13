package ranking

import (
	"context"
	"errors"
	"testing"

	"bd2server/internal/server/storage/shared"
)

func arcadePublication(account string, owner, version uint64, records ...ArcadeRecord) ArcadePublication {
	return ArcadePublication{Account: account, Version: version, Profile: ArcadeProfile{Owner: owner, Name: account}, Records: records}
}

func arcadeResult(family string, event, stage, mode uint64, score float64, achieved uint64) ArcadeRecord {
	return ArcadeRecord{ArcadeScope: ArcadeScope{Family: family, EventUID: event, Stage: stage, Mode: mode}, Score: score, AchievedAt: achieved}
}

func TestArcadePersistentBoardAndPendingOwnImprovement(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, err := shared.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	ranks, err := NewArcade(ctx, testPVPStore{store})
	if err != nil {
		t.Fatal(err)
	}
	alice := arcadePublication("alice", 11, 1, arcadeResult("Survival", 7, 1, 0, 50, 100))
	bob := arcadePublication("bob", 22, 1, arcadeResult("Survival", 7, 2, 0, 100, 101))
	carol := arcadePublication("carol", 33, 1, arcadeResult("Survival", 7, 3, 0, 150, 102))
	for _, p := range []ArcadePublication{alice, bob, carol} {
		if err := ranks.Publish(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = shared.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	ranks, err = NewArcade(ctx, testPVPStore{store})
	if err != nil {
		t.Fatal(err)
	}
	scope := ArcadeScope{Family: "Survival", EventUID: 7}
	rows := ranks.Board(scope, alice.Profile, alice.Records, 1)
	if len(rows) != 2 || rows[0].Profile.Name != "carol" || rows[0].Rank != 1 || rows[1].Profile.Name != "alice" || rows[1].Rank != 3 || rows[1].TopPercent != 100 || !rows[1].Own {
		t.Fatalf("persisted multiplayer/own board: %+v", rows)
	}
	alice.Version = 2
	alice.Records[0].Score, alice.Records[0].AchievedAt = 200, 200
	rows = ranks.Board(scope, alice.Profile, alice.Records, 1)
	if len(rows) != 1 || rows[0].Profile.Name != "alice" || rows[0].Rank != 1 || rows[0].TopPercent != 100.0/3 || !rows[0].Own {
		t.Fatalf("pending own result not counted once: %+v", rows)
	}
	otherView := ranks.Board(scope, bob.Profile, bob.Records, 3)
	if otherView[0].Profile.Name != "carol" {
		t.Fatalf("unpublished own overlay escaped to other players: %+v", otherView)
	}
	if err := ranks.Publish(ctx, alice); err != nil {
		t.Fatal(err)
	}
	if err := ranks.Publish(ctx, alice); err != nil {
		t.Fatal("outbox retry:", err)
	}
	if err := ranks.Publish(ctx, arcadePublication("alice", 11, 1, arcadeResult("Survival", 7, 1, 0, 50, 100))); err != nil {
		t.Fatal("stale outbox retry:", err)
	}
	rows = ranks.Board(scope, bob.Profile, bob.Records, 3)
	if len(rows) != 3 || rows[0].Profile.Name != "alice" || rows[0].Score != 200 || rows[2].Profile.Name != "bob" || rows[2].Rank != 3 {
		t.Fatalf("published result lost across retries: %+v", rows)
	}
	alice.Version = 3
	alice.Profile.Name, alice.Profile.Title = "Alice renamed", 9
	if err := ranks.Publish(ctx, alice); err != nil {
		t.Fatal(err)
	}
	rows = ranks.Board(scope, bob.Profile, bob.Records, 3)
	if rows[0].Profile.Name != "Alice renamed" || rows[0].Profile.Title != 9 || rows[0].Score != 200 {
		t.Fatalf("profile publication replaced score: %+v", rows)
	}
	if foreign := ranks.Board(ArcadeScope{Family: "Survival", EventUID: 8}, alice.Profile, alice.Records, 100); len(foreign) != 0 {
		t.Fatalf("season leaked: %+v", foreign)
	}
}

func TestArcadeSichuanFasterTimesAndFirstAchievementTies(t *testing.T) {
	ctx := context.Background()
	ranks, err := NewArcade(ctx, teamTestStore(t))
	if err != nil {
		t.Fatal(err)
	}
	alice := arcadePublication("alice", 11, 1, arcadeResult("Sichuan", 7, 1, 2, 12.25, 200))
	bob := arcadePublication("bob", 22, 1, arcadeResult("Sichuan", 7, 1, 2, 12.25, 100))
	carol := arcadePublication("carol", 33, 1, arcadeResult("Sichuan", 7, 1, 2, 11.75, 300))
	for _, p := range []ArcadePublication{alice, bob, carol} {
		if err := ranks.Publish(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	rows := ranks.Board(ArcadeScope{Family: "Sichuan", EventUID: 7}, alice.Profile, nil, 100)
	if len(rows) != 3 || rows[0].Profile.Name != "carol" || rows[0].Score != 11.75 || rows[1].Profile.Name != "bob" || rows[2].Profile.Name != "alice" {
		t.Fatalf("LocalText125517 completion time and first-achievement order: %+v", rows)
	}
	regressed := alice
	regressed.Version = 2
	regressed.Records = append([]ArcadeRecord(nil), alice.Records...)
	regressed.Records[0].Score = 13.25
	if err := ranks.Publish(ctx, regressed); err == nil {
		t.Fatal("slower challenge replaced saved best")
	}
}

func TestArcadeRhythmModesStayIndependentAndNormalIsUnranked(t *testing.T) {
	ctx := context.Background()
	ranks, err := NewArcade(ctx, teamTestStore(t))
	if err != nil {
		t.Fatal(err)
	}
	alice := arcadePublication("alice", 11, 1, arcadeResult("Rhythm", 7, 1, 1, 100, 100), arcadeResult("Rhythm", 7, 1, 2, 20, 101))
	bob := arcadePublication("bob", 22, 1, arcadeResult("Rhythm", 7, 1, 1, 10, 100), arcadeResult("Rhythm", 7, 1, 2, 200, 101))
	for _, p := range []ArcadePublication{alice, bob} {
		if err := ranks.Publish(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for mode, leader := range map[uint64]string{1: "alice", 2: "bob"} {
		rows := ranks.Board(ArcadeScope{Family: "Rhythm", EventUID: 7, Stage: 1, Mode: mode}, alice.Profile, nil, 100)
		if len(rows) != 2 || rows[0].Profile.Name != leader {
			t.Fatalf("rhythm mode %d merged: %+v", mode, rows)
		}
	}
	normal := arcadePublication("normal", 44, 1, arcadeResult("Rhythm", 7, 1, 0, 1000, 100))
	if err := ranks.Publish(ctx, normal); err == nil {
		t.Fatal("normal entered leaderboard despite LocalText1500089")
	}
}

func TestArcadeHopscotchCompletedTimeKeepsCorrespondingArea(t *testing.T) {
	ctx := context.Background()
	ranks, err := NewArcade(ctx, teamTestStore(t))
	if err != nil {
		t.Fatal(err)
	}
	fast := arcadeResult("Hopscotch", 8, 1, 0, 5000, 100)
	fast.ClearTime, fast.CapturedArea = 5000, 80
	slow := arcadeResult("Hopscotch", 8, 1, 0, 6000, 101)
	slow.ClearTime, slow.CapturedArea = 6000, 99
	if err := ranks.Publish(ctx, arcadePublication("fast", 55, 1, fast)); err != nil {
		t.Fatal(err)
	}
	if err := ranks.Publish(ctx, arcadePublication("slow", 66, 1, slow)); err != nil {
		t.Fatal(err)
	}
	rows := ranks.Board(fast.ArcadeScope, ArcadeProfile{}, nil, 100)
	if len(rows) != 2 || rows[0].Profile.Name != "fast" || rows[0].ClearTime != 5000 || rows[0].CapturedArea != 80 {
		t.Fatalf("hopscotch time replaced by area: %+v", rows)
	}
}

type arcadeFailingStore struct {
	Store
	fail bool
}

func (s *arcadeFailingStore) Update(ctx context.Context, write func(Tx) error) error {
	return s.Store.Update(ctx, func(tx Tx) error {
		if err := write(tx); err != nil {
			return err
		}
		if s.fail {
			return errors.New("injected commit failure")
		}
		return nil
	})
}

func TestArcadeFailedPersistenceDoesNotAdvanceProjection(t *testing.T) {
	ctx := context.Background()
	store := &arcadeFailingStore{Store: teamTestStore(t)}
	ranks, err := NewArcade(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	publication := arcadePublication("alice", 11, 1, arcadeResult("PackMan", 9, 1, 1, 500, 100))
	store.fail = true
	if err := ranks.Publish(ctx, publication); err == nil {
		t.Fatal("injected persistence failure ignored")
	}
	if rows := ranks.Board(publication.Records[0].ArcadeScope, ArcadeProfile{}, nil, 100); len(rows) != 0 {
		t.Fatalf("failed write advanced cache: %+v", rows)
	}
	reopened, err := NewArcade(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if scopes := reopened.Scopes(); len(scopes) != 0 {
		t.Fatalf("failed write survived reopen: %+v", scopes)
	}
	store.fail = false
	if err := reopened.Publish(ctx, publication); err != nil {
		t.Fatal("retry:", err)
	}
	if err := reopened.Publish(ctx, publication); err != nil {
		t.Fatal("duplicate retry:", err)
	}
	reloaded, err := NewArcade(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	rows := reloaded.Board(publication.Records[0].ArcadeScope, publication.Profile, nil, 100)
	if len(rows) != 1 || rows[0].Rank != 1 || rows[0].Score != 500 || !rows[0].Own {
		t.Fatalf("retry did not publish once: %+v", rows)
	}
}
