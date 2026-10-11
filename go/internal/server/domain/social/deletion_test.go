package social_test

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/social"
	"context"
	"errors"
	"testing"
	"time"
)

func TestGuildDeletionDueWorkCancelsAndSurvivesOwnerRestart(t *testing.T) {
	ctx := context.Background()
	owner, store := newOwner(t)
	alice := member("alice", 1)
	id := guild(t, owner, alice, "TimedGuild", social.OpenJoin)
	commit(t, owner, social.Operation{ID: "delete:first", Kind: social.Leave, Actor: alice, GuildID: id, ExpectedMembershipEpoch: 1, AtMS: epoch.UnixMilli()})
	dueAt := epoch.Add(time.Hour).UnixMilli()
	before, _, err := owner.DueDeletions(ctx, dueAt-1, "", 128)
	if err != nil || len(before) != 0 {
		t.Fatalf("early deletion became due: %v %v", before, err)
	}
	commit(t, owner, social.Operation{ID: "delete:cancel", Kind: social.CancelDelete, Actor: alice, GuildID: id, ExpectedMembershipEpoch: 1, AtMS: epoch.Add(time.Minute).UnixMilli()})
	if rows, _, err := owner.DueDeletions(ctx, dueAt, "", 128); err != nil || len(rows) != 0 {
		t.Fatalf("cancelled deletion stayed scheduled: %v %v", rows, err)
	}
	commit(t, owner, social.Operation{ID: "delete:second", Kind: social.Leave, Actor: alice, GuildID: id, ExpectedMembershipEpoch: 1, AtMS: epoch.Add(2 * time.Minute).UnixMilli()})
	// Reuse the persisted store with a new owner; timers belong to shared state.
	restarted, err := social.New(store, &gamedata.GuildDesign{Guild: gamedata.GuildRules{MaxMembers: 3, LeaveDelayHours: 2}})
	if err != nil {
		t.Fatal(err)
	}
	at := dueAt + 2*time.Minute.Milliseconds()
	due, _, err := restarted.DueDeletions(ctx, at, "", 128)
	if err != nil || len(due) != 1 || due[0] != id {
		t.Fatalf("persisted deletion missing: %v %v", due, err)
	}
	if err := restarted.FinalizeDeletion(ctx, id, at); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Guild(ctx, id); !errors.Is(err, social.ErrNotFound) {
		t.Fatalf("finalized guild still visible: %v", err)
	}
	account, err := restarted.Account(ctx, alice.AccountID)
	if err != nil || account.GuildID != 0 || account.Epoch != 2 || account.LeaveUntilMS <= at {
		t.Fatalf("deletion failed to release member with cooldown: %+v %v", account, err)
	}
	if rows, _, err := restarted.DueDeletions(ctx, at, "", 128); err != nil || len(rows) != 0 {
		t.Fatalf("finalized deletion stayed scheduled: %v %v", rows, err)
	}
}
