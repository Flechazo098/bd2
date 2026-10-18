package ranking

import (
	"bd2server/internal/server/domain/battle/roguelike"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/shared"
	"context"
	"testing"
)

func TestRogueLikeCachedScoresRetryReopenAndPendingOwnRank(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, e := shared.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = store.Close() }()
	rank, e := NewRogueLike(ctx, testPVPStore{store})
	if e != nil {
		t.Fatal(e)
	}
	alice := roguelike.RankPublication{Account: "alice", Season: 1, Version: 1, Entry: roguelike.RankEntry{Owner: 1, Score: 600, Damage: 20}}
	bob := roguelike.RankPublication{Account: "bob", Season: 1, Version: 1, Entry: roguelike.RankEntry{Owner: 2, Score: 400, Damage: 10}}
	for _, p := range []roguelike.RankPublication{alice, bob, alice} {
		if e = rank.Publish(ctx, p); e != nil {
			t.Fatal(e)
		}
	}
	total, e := rank.Total(command.Context{}, 1)
	if e != nil || total != 1000 {
		t.Fatalf("duplicate delivery changed contribution: %d %v", total, e)
	}
	pendingTotal, e := rank.PendingTotal(command.Context{}, 1, roguelike.RankEntry{Owner: 2, Score: 700})
	if e != nil || pendingTotal != 1300 {
		t.Fatal("own committed settlement was omitted before projection delivery")
	}
	pending := bob.Entry
	pending.Damage = 30
	top, mine, e := rank.Snapshot(command.Context{}, 1, pending)
	if e != nil || len(top) != 2 || top[0].Owner != 2 || mine.Rank != 1 {
		t.Fatalf("pending own record did not overlay board: %+v %+v %v", top, mine, e)
	}
	top[0].Score = 99
	again, _, _ := rank.Snapshot(command.Context{}, 1, alice.Entry)
	if again[0].Owner != 1 {
		t.Fatal("local unpublished overlay mutated shared board")
	}
	bob.Version++
	bob.Entry.Damage = 30
	bob.Entry.Score = 700
	if e = rank.Publish(ctx, bob); e != nil {
		t.Fatal(e)
	}
	total, _ = rank.Total(command.Context{}, 1)
	if total != 1300 {
		t.Fatal("improvement counted the owner's whole score again")
	}
	if e = store.Close(); e != nil {
		t.Fatal(e)
	}
	store, e = shared.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	rank, e = NewRogueLike(ctx, testPVPStore{store})
	if e != nil {
		t.Fatal(e)
	}
	total, _ = rank.Total(command.Context{}, 1)
	top, mine, e = rank.Snapshot(command.Context{}, 1, alice.Entry)
	if e != nil || total != 1300 || len(top) != 2 || top[0].Owner != 2 || mine.Rank != 2 {
		t.Fatal("restored score sum or board lost committed results")
	}
	empty, _ := rank.Total(command.Context{}, 2)
	if empty != 0 {
		t.Fatal("ranking total leaked into next season")
	}
	bob.Version++
	bob.Entry.Score = 100
	if e = rank.Publish(ctx, bob); e == nil {
		t.Fatal("accepted regressed contribution")
	}
}
