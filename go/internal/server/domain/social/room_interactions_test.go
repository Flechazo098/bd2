package social_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/profile"
	"bd2server/internal/server/domain/social"
	"bd2server/internal/server/protocol/wire"
)

func TestRoomPopularityResetsMonthlyPerViewerWithoutResettingTotal(t *testing.T) {
	ctx := context.Background()
	store := &memoryStore{rows: map[string]map[string][]byte{}}
	now := time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC)
	rooms := &gamedata.RoomDesign{Defaults: []uint64{1}, Limits: map[uint64]gamedata.RoomLimits{1: {Characters: 4}}, ListCount: 10}
	friends, err := social.NewFriends(store, gamedata.FriendRules{Friends: 50, Sent: 50, Received: 50, Recommended: 10, ExpireDays: 7}, gamedata.SupporterRules{}, rooms, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for index := uint64(1); index <= 3; index++ {
		if err := friends.Publish(ctx, social.FriendProfile{AccountID: fmt.Sprint(index), OwnerIndex: index, Version: 1, Snapshot: profile.Snapshot{OwnerIndex: index, UserID: fmt.Sprint(index)}}); err != nil {
			t.Fatal(err)
		}
	}
	seq := uint64(0)
	call := func(actor uint64, path string, fields []byte) ([]byte, error) {
		seq++
		_, body, err := friends.Execute(ctx, command.Identity{AccountID: fmt.Sprint(actor), SessionID: "room", RequestID: fmt.Sprint(seq)}, actor, path, append(wire.AppendVarint(nil, 1, seq), fields...), 0)
		return body, err
	}
	like := func(actor, target uint64) ([]byte, error) {
		return call(actor, "/MyRoomLike", wire.AppendVarint(nil, 2, target))
	}
	if _, err := like(1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := like(1, 3); err != nil {
		t.Fatal("monthly room allowance was incorrectly global", err)
	}
	if _, err := like(3, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := like(1, 2); err == nil {
		t.Fatal("same viewer could increase popularity twice in a month")
	}
	popular, err := call(1, "/MyRoomSearchRecommend", wire.AppendVarint(nil, 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	var order []uint64
	if err := wire.Walk(popular, func(field wire.Field) error {
		if field.Number == 1 {
			owner, _, err := wire.Varint(field.Value, 1)
			order = append(order, owner)
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != 2 || order[1] != 3 {
		t.Fatalf("monthly popular order=%v want[2 3]", order)
	}
	// Current LocalText140304 says monthly popularity resets at09:00KST,
	// and140200 says the same user's room may be liked again next month.
	now = now.Add(2 * time.Minute)
	before, _, err := friends.RoomProfile(ctx, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := wire.Varint(before, 10)
	if err != nil || last == 0 {
		t.Fatal("viewer history was lost during month rollover")
	}
	result, err := like(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	monthly, _, err := wire.Varint(result, 1)
	if err != nil {
		t.Fatal(err)
	}
	total, _, err := wire.Varint(result, 2)
	if err != nil {
		t.Fatal(err)
	}
	if monthly != 1 || total != 3 {
		t.Fatalf("new month popularity=%d/%d want1/3", monthly, total)
	}
	count, err := friends.GameplayCounts(ctx, "2", 2)
	if err != nil || count.RoomLikes != 3 {
		t.Fatalf("actual room achievement popularity=%+v error=%v", count, err)
	}
	if profileLikes, err := friends.LikeCount(ctx, 2); err != nil || profileLikes != 0 {
		t.Fatalf("room popularity contaminated profile likes=%d error=%v", profileLikes, err)
	}
	if _, err := call(2, "/MyRoomShareOptionUpdate", wire.AppendVarint(nil, 2, 4)); err != nil {
		t.Fatal(err)
	}
	if _, err := like(3, 2); err == nil {
		t.Fatal("private room accepted a monthly popularity increase")
	}
	private, _, err := friends.RoomProfile(ctx, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.Walk(private, func(field wire.Field) error {
		if field.Number == 8 {
			t.Fatal("private profile projection exposed layout")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
