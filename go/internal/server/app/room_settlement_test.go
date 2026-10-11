package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"bd2server/internal/server/domain/command"
	eventplay "bd2server/internal/server/domain/events/play"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/room"
	accountstate "bd2server/internal/server/storage/account"
	"bd2server/internal/server/storage/outbox"
	"bd2server/internal/server/storage/shared"
)

func TestRoomResultSurvivesRestartAndClaimsRemainBoundToParticipants(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, err := shared.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if store != nil {
			if err := store.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	r, err := openRoomResults(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	final := room.FinalResult{Clear: true, ScoreMilliseconds: 65432, View: room.View{InstanceID: "finished-party", Kind: room.Action, Selection: room.Selection{EventScheduleID: 100, MonsterID: 200}, StartTime: time.UnixMilli(1000), EndTime: time.UnixMilli(66432), Members: []room.MemberView{
		{Principal: room.Principal{AccountID: "alice", OwnerIndex: 1, SessionID: "old-a", Generation: 1}, NativeGUID: "alice-run", CharacterID: 11, Ready: true, Profile: room.Profile{UserInfo: wire.AppendString(nil, 4, "Alice")}},
		{Principal: room.Principal{AccountID: "bob", OwnerIndex: 2, SessionID: "old-b", Generation: 2}, NativeGUID: "bob-run", CharacterID: 12, Ready: true, Profile: room.Profile{UserInfo: wire.AppendString(nil, 4, "Bob")}},
	}}}
	rawFinal, err := json.Marshal(final)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a stop after the final commits but before its ranking is published.
	if err := store.Update(ctx, func(tx *shared.Tx) error {
		return tx.Put("room.results", final.View.InstanceID, rawFinal)
	}); err != nil {
		t.Fatal(err)
	}
	beforeRanking, err := r.PrepareResults(ctx, "alice", []eventplay.NativeRun{{InstanceID: "finished-party", GUID: "alice-run", Family: "Action"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := beforeRanking.Result(command.Context{Identity: command.Identity{AccountID: "alice"}}, eventplay.NativeRun{InstanceID: "finished-party", GUID: "alice-run", Family: "Action"}); !errors.Is(err, eventplay.ErrRoomNotFinished) {
		t.Fatalf("unpublished ranking exposed claimable result: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	store, err = shared.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	r, err = openRoomResults(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	alice := command.Context{Identity: command.Identity{AccountID: "alice", SessionID: "new-login"}}
	run := eventplay.NativeRun{InstanceID: "finished-party", GUID: "alice-run", Family: "Action", SessionID: "old-a", UID: 100, Monster: 200}
	resultFor := func(identity command.Context, run eventplay.NativeRun) (eventplay.NativeResult, error) {
		t.Helper()
		prepared, err := r.PrepareResults(ctx, identity.AccountID, []eventplay.NativeRun{run})
		if err != nil {
			t.Fatal(err)
		}
		return prepared.Result(identity, run)
	}
	result, err := resultFor(alice, run)
	if err != nil || !result.Clear || result.Score != 65432 {
		t.Fatalf("committed completion unavailable after restart: %+v %v", result, err)
	}
	intruder := command.Context{Identity: command.Identity{AccountID: "mallory"}}
	if _, err := resultFor(intruder, run); !errors.Is(err, room.ErrUnauthorized) {
		t.Fatalf("nonparticipant read another player's result: %v", err)
	}
	if err := r.persist(ctx, final); err != nil {
		t.Fatal(err)
	}
	ranks, err := r.NativeRankings(alice, 100, 200, 2)
	if err != nil || len(ranks) != 1 || ranks[0].Score != 65432 || len(ranks[0].Members) != 2 {
		t.Fatalf("retry changed canonical team record: %+v %v", ranks, err)
	}
	source, err := accountstate.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := source.Close(); err != nil {
			t.Error(err)
		}
	})
	publish := func(account, topic string, run eventplay.NativeRun) outbox.Pending {
		t.Helper()
		tx, err := source.BeginCommand(ctx)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		if err := outbox.Publish(tx, account, command.Event{ID: topic + ":" + account, Topic: topic, Payload: payload}); err != nil {
			t.Fatal(err)
		}
		pending, err := outbox.Read(tx, 32)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		for _, event := range pending {
			if event.Envelope.Source == account && event.Envelope.Event.Topic == topic {
				return event
			}
		}
		t.Fatal("committed source claim missing")
		return outbox.Pending{}
	}
	if err := r.Consume(ctx, publish("alice", "room.run.accepted", run)); err != nil {
		t.Fatal(err)
	}
	claimA := publish("alice", "room.run.claimed", run)
	if err := r.Consume(ctx, claimA); err != nil {
		t.Fatal(err)
	}
	if err := r.Consume(ctx, claimA); err != nil {
		t.Fatal(err)
	}
	bob := command.Context{Identity: command.Identity{AccountID: "bob", SessionID: "new-b"}}
	run.GUID = "bob-run"
	run.SessionID = "old-b"
	if _, err := resultFor(bob, run); err != nil {
		t.Fatalf("first player's claim removed second player's entitlement: %v", err)
	}
	if err := r.Consume(ctx, publish("bob", "room.run.accepted", run)); err != nil {
		t.Fatal(err)
	}
	claimB := publish("bob", "room.run.claimed", run)
	if err := r.Consume(ctx, claimB); err != nil {
		t.Fatal(err)
	}
	if err := r.Consume(ctx, claimA); err != nil {
		t.Fatalf("claim replay failed after cleanup: %v", err)
	}
	if err := r.persist(ctx, final); err != nil {
		t.Fatalf("persistence retry failed after cleanup: %v", err)
	}
	if err := store.View(ctx, func(tx *shared.Tx) error {
		for _, bucket := range []string{"room.results", "room.ready", "room.runs", "room.claims"} {
			rows, err := tx.Scan(bucket, "", "", 128)
			if err != nil {
				return err
			}
			if len(rows) != 0 {
				t.Errorf("fully claimed room retained %s payloads: %d", bucket, len(rows))
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r, err = openRoomResults(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resultFor(bob, run); !errors.Is(err, eventplay.ErrRoomAbandoned) {
		t.Fatalf("fully acknowledged result retained: %v", err)
	}
	ranks, err = r.NativeRankings(alice, 100, 200, 2)
	if err != nil || len(ranks) != 1 {
		t.Fatalf("claim cleanup lost season record: %+v %v", ranks, err)
	}
}
