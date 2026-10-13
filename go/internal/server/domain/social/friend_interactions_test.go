package social_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/profile"
	"bd2server/internal/server/domain/social"
	"bd2server/internal/server/protocol/wire"
)

func TestProfileLikesAndRentalClaimsAcrossDailyReset(t *testing.T) {
	ctx := context.Background()
	store := &memoryStore{rows: map[string]map[string][]byte{}}
	// Independently inspected 20261008155037 SupportCharacterDefaultTable 0:
	// fields3/2/1=3/3/10, reward fields7/6/8=44/0/2. LocalText6711
	// explicitly preserves unclaimed rentals after the daily reset.
	rules := gamedata.SupporterRules{DailyUses: 3, DailyRecommendedUses: 3, DailyRewardClaims: 10, Reward: gamedata.Reward{Type: 44, Count: 2}}
	now := time.Date(2026, 10, 12, 23, 59, 0, 0, time.UTC)
	friends, err := social.NewFriends(store, gamedata.FriendRules{Friends: 50, Sent: 50, Received: 50, Recommended: 10, ExpireDays: 7}, rules, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	owner, err := social.New(store, &gamedata.GuildDesign{Guild: gamedata.GuildRules{MaxMembers: 20}, Supporters: rules})
	if err != nil {
		t.Fatal(err)
	}
	for index := uint64(1); index <= 5; index++ {
		slot := wire.AppendVarint(nil, 1, index)
		slot = wire.AppendVarint(slot, 3, 101)
		slot = wire.AppendString(slot, 6, base64.StdEncoding.EncodeToString(wire.AppendBytes(nil, 1, wire.AppendVarint(nil, 1, index+10))))
		if err := friends.Publish(ctx, social.FriendProfile{AccountID: fmt.Sprint(index), OwnerIndex: index, Version: 1, Snapshot: profile.Snapshot{OwnerIndex: index, UserID: fmt.Sprint(index), Supporters: []profile.CostumeBase{{ID: 101, DesignID: 5}, {ID: 201, DesignID: 6}}}, SupporterDetails: map[uint64][]byte{0: wire.AppendBytes(nil, 1, slot)}}); err != nil {
			t.Fatal(err)
		}
	}
	seq := uint64(0)
	call := func(actor uint64, path string, fields []byte) ([]byte, error) {
		seq++
		_, body, err := friends.Execute(ctx, command.Identity{AccountID: fmt.Sprint(actor), SessionID: "daily", RequestID: fmt.Sprint(seq)}, actor, path, append(wire.AppendVarint(nil, 1, seq), fields...), 0)
		return body, err
	}
	for actor := uint64(1); actor <= 5; actor++ {
		if actor == 2 {
			continue
		}
		if _, err := call(actor, "/SupporterBattleInfo", wire.AppendVarint(nil, 2, 1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := call(1, "/LikeUser", wire.AppendVarint(nil, 2, 2)); err != nil {
		t.Fatal(err)
	}
	if _, err := call(1, "/LikeUser", wire.AppendVarint(nil, 2, 3)); err == nil {
		t.Fatal("daily quota was per target instead of global")
	}
	// Two earlier unclaimed uses remain eligible after the reset; today's
	// twelfth use must remain visible without creating an extra entitlement.
	for range 2 {
		if _, err := call(1, "/SupporterBorrow", wire.AppendVarint(nil, 2, 2)); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(24 * time.Hour)
	var histories []uint64
	var totalHistories int
	for actor := uint64(1); actor <= 5; actor++ {
		if actor == 2 {
			continue
		}
		for range 3 {
			if _, err := call(actor, "/SupporterBorrow", wire.AppendVarint(nil, 2, 2)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := call(1, "/SupporterBorrow", wire.AppendVarint(nil, 2, 2)); err == nil {
		t.Fatal("recommended rental quota was not enforced")
	}
	// Path of Adventure has an independent allowance for the same lender.
	independent := command.Identity{AccountID: "1", SessionID: "daily", RequestID: "adventure-picker"}
	if _, _, err := friends.Execute(ctx, independent, 1, "/SupporterBattleInfo", wire.AppendVarint(wire.AppendVarint(nil, 1, 1000), 2, 9), 5); err != nil {
		t.Fatal(err)
	}
	independent.RequestID = "adventure-borrow"
	if _, _, err := friends.Execute(ctx, independent, 1, "/SupporterBorrow", wire.AppendVarint(wire.AppendVarint(nil, 1, 1001), 2, 2), 5); err != nil {
		t.Fatalf("story quota blocked independent adventure allowance: %v", err)
	}
	if _, err := call(1, "/SupporterBattleInfo", wire.AppendVarint(nil, 2, 1)); err != nil {
		t.Fatal(err)
	}
	status, err := call(2, "/SupporterStatus", wire.AppendVarint(nil, 2, 50))
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.Walk(status, func(f wire.Field) error {
		if f.Number == 1 {
			totalHistories++
			id, _, err := wire.Varint(f.Value, 1)
			if err != nil {
				return err
			}
			received, _, err := wire.Varint(f.Value, 12)
			if err != nil {
				return err
			}
			if received == 0 {
				histories = append(histories, id)
			}
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if totalHistories != 15 {
		t.Fatalf("visible rental history=%d want15", totalHistories)
	}
	if len(histories) != 12 {
		t.Fatalf("history count=%d want12 eligible", len(histories))
	}
	claim := func(account string, index uint64, id string, ids []uint64) (social.Reservation, error) {
		return owner.Prepare(ctx, social.Operation{ID: id, Kind: social.SupporterClaim, Actor: member(account, index), AtMS: now.UnixMilli(), Day: now.Format("2006-01-02"), UsageIDs: ids})
	}
	if _, err := claim("1", 1, "steal", histories[:1]); !errors.Is(err, social.ErrIneligible) {
		t.Fatalf("other lender's rental claim error=%v", err)
	}
	r, err := claim("2", 2, "daily-ten", histories[:10])
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Grants) != 1 || r.Grants[0] != (gamedata.Reward{Type: 44, Count: 20}) {
		t.Fatalf("ten rental rewards=%+v", r.Grants)
	}
	if _, err := claim("2", 2, "parallel", histories[10:]); !errors.Is(err, social.ErrPending) {
		t.Fatalf("prepared claim did not isolate simultaneous rewards: %v", err)
	}
	if _, err := owner.Cancel(ctx, proof(r)); err != nil {
		t.Fatal(err)
	}
	r, err = claim("2", 2, "daily-ten-retry", histories[:10])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Confirm(ctx, proof(r)); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Confirm(ctx, proof(r)); err != nil {
		t.Fatal(err)
	}
	if _, err := claim("2", 2, "daily-overflow", histories[10:]); !errors.Is(err, social.ErrCapacity) {
		t.Fatalf("daily reward ceiling error=%v", err)
	}
	if available, err := friends.SupporterRewardAvailable(ctx, 2); err != nil || available {
		t.Fatalf("reward badge ignored daily ceiling available=%v error=%v", available, err)
	}
	now = now.Add(2 * time.Minute)
	like, err := call(1, "/MyLikeInfo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(like) != 0 {
		t.Fatal("daily like target did not reset")
	}
	if _, err := call(1, "/LikeUser", wire.AppendVarint(nil, 2, 2)); err != nil {
		t.Fatal(err)
	}
	if count, err := friends.LikeCount(ctx, 2); err != nil || count != 2 {
		t.Fatalf("received total lost across daily reset count=%d error=%v", count, err)
	}
	if available, err := friends.SupporterRewardAvailable(ctx, 2); err != nil || !available {
		t.Fatalf("unclaimed history did not survive daily reset available=%v error=%v", available, err)
	}
	r, err = claim("2", 2, "old-unclaimed", histories[10:])
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Grants) != 1 || r.Grants[0].Count != 4 {
		t.Fatal("old unclaimed rentals lost their reward entitlement")
	}
	if _, err := owner.Confirm(ctx, proof(r)); err != nil {
		t.Fatal(err)
	}
	if _, err := claim("2", 2, "already-claimed", histories[:1]); !errors.Is(err, social.ErrIneligible) {
		t.Fatalf("claimed history reused across reset: %v", err)
	}
	if _, err := call(1, "/SupporterBorrow", wire.AppendVarint(nil, 2, 2)); err != nil {
		t.Fatalf("lender rental uses did not reset: %v", err)
	}
}
