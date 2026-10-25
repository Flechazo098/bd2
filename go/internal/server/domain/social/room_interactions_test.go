package social_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/profile"
	"bd2server/internal/server/domain/roster"
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

// MyRoomSettingUI subtracts use counts from inventory across all rooms.
// MyRoomPacket.RecvMyRoomMoveResponse moves the representative ID with its layout.
func TestRoomLayoutUsageAndRepresentativeSurviveReloadAndMove(t *testing.T) {
	ctx := context.Background()
	store := &memoryStore{rows: map[string]map[string][]byte{}}
	design := &gamedata.RoomDesign{Defaults: []uint64{1, 2}, Limits: map[uint64]gamedata.RoomLimits{1: {Objects: 4, Characters: 4}, 2: {Objects: 4, Characters: 4}}, Items: map[uint64]gamedata.RoomItem{201: {Kind: 1}}}
	open := func() *social.Friends {
		t.Helper()
		f, err := social.NewFriends(store, gamedata.FriendRules{Friends: 50, Sent: 50, Received: 50, Recommended: 10, ExpireDays: 7}, gamedata.SupporterRules{}, design, nil)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	f := open()
	owner := social.FriendProfile{AccountID: "1", OwnerIndex: 1, Version: 1, Snapshot: profile.Snapshot{OwnerIndex: 1}, RoomItems: []inventory.Item{{Type: 27, ID: 201, InvenIndex: 1010, Count: 2, UseCount: 99}, {Type: 28, ID: 301, InvenIndex: 3030, Count: 1}}, RoomCostumes: []roster.Costume{{ID: 101, InvenIndex: 2020}}}
	if err := f.Publish(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := f.Publish(ctx, social.FriendProfile{AccountID: "2", OwnerIndex: 2, Version: 1, Snapshot: profile.Snapshot{OwnerIndex: 2}}); err != nil {
		t.Fatal(err)
	}
	seq := uint64(0)
	call := func(path string, fields []byte) []byte {
		t.Helper()
		seq++
		_, body, err := f.Execute(ctx, command.Identity{AccountID: "1", SessionID: "usage", RequestID: fmt.Sprint(seq)}, 1, path, append(wire.AppendVarint(nil, 1, seq), fields...), 0)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	position := func(index, typ uint64) []byte {
		return wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, index), 2, typ), 3, 1)
	}
	room := func(id uint64, name string, hidden bool, positions ...[]byte) []byte {
		out := wire.AppendString(wire.AppendVarint(nil, 1, id), 2, name)
		if hidden {
			out = wire.AppendVarint(out, 4, 1)
		}
		for _, p := range positions {
			out = wire.AppendBytes(out, 3, p)
		}
		return out
	}
	saved := wire.AppendBytes(nil, 2, room(1, "Representative", false, position(1010, 0), position(2020, 1)))
	saved = wire.AppendBytes(saved, 2, room(2, "Hidden", true, position(1010, 0), position(3030, 2)))
	call("/MyRoomSave", saved)
	f = open()
	owner.Version++
	if err := f.Publish(ctx, owner); err != nil {
		t.Fatal(err)
	}
	rows := func(raw []byte, field int) [][]byte {
		t.Helper()
		var result [][]byte
		if err := wire.Walk(raw, func(v wire.Field) error {
			if v.Number == field {
				result = append(result, v.Value)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return result
	}
	value := func(raw []byte, field int) uint64 {
		t.Helper()
		v, _, err := wire.Varint(raw, field)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	assertUsage := func(furniture, costume, trophy uint64) {
		t.Helper()
		response := call("/MyRoomUserInfo", wire.AppendVarint(nil, 2, 1))
		if value(response, 2) != 1 {
			t.Fatal("own room response did not synchronize global inventory usage")
		}
		info := rows(response, 1)[0]
		if got := value(rows(info, 5)[0], 10); got != furniture {
			t.Fatalf("furnishing usage=%d want %d", got, furniture)
		}
		if got := value(rows(info, 6)[0], 7); got != costume {
			t.Fatalf("costume usage=%d want %d", got, costume)
		}
		if got := value(rows(info, 7)[0], 5); got != trophy {
			t.Fatalf("trophy usage=%d want %d", got, trophy)
		}
		if got := value(rows(call("/MyRoomItemInfo", nil), 1)[0], 5); got != trophy {
			t.Fatalf("trophy inventory usage=%d want %d", got, trophy)
		}
	}
	assertUsage(2, 1, 1)
	call("/MyRoomMove", wire.AppendVarint(wire.AppendVarint(nil, 2, 1), 3, 2))
	f = open()
	info, _, err := f.RoomProfile(ctx, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if value(info, 4) != 2 {
		t.Fatal("representative ID did not follow the moved room")
	}
	visible := rows(info, 8)
	if len(visible) != 1 || value(visible[0], 1) != 2 || len(rows(visible[0], 3)) != 2 {
		t.Fatal("visitor lost the moved representative layout")
	}
	assertUsage(2, 1, 1)
	call("/MyRoomSave", wire.AppendBytes(nil, 2, room(1, "Empty", true)))
	assertUsage(1, 1, 0)
	call("/MyRoomSave", wire.AppendBytes(nil, 2, room(2, "Empty representative", false)))
	assertUsage(0, 0, 0)
}

// MyRoomPacket.GetPresetInfoForStore replaces a visited hidden wallpaper with
// the viewer's PresetHiddenWallItemId inventory index before sending the preset.
func TestVisitedRoomPresetAcceptsOnlyHiddenWallpaperSubstitution(t *testing.T) {
	ctx := context.Background()
	store := &memoryStore{rows: map[string]map[string][]byte{}}
	design := &gamedata.RoomDesign{Defaults: []uint64{1}, Limits: map[uint64]gamedata.RoomLimits{1: {}}, UserPresets: 10, HiddenWallItemID: 501,
		Items: map[uint64]gamedata.RoomItem{201: {Location: 1}, 501: {Location: 1}},
	}
	f, err := social.NewFriends(store, gamedata.FriendRules{Friends: 50, Sent: 50, Received: 50, Recommended: 10, ExpireDays: 7}, gamedata.SupporterRules{}, design, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []social.FriendProfile{
		{AccountID: "1", OwnerIndex: 1, Version: 1, Snapshot: profile.Snapshot{OwnerIndex: 1}, RoomItems: []inventory.Item{{Type: 27, ID: 201, InvenIndex: 1010, Count: 2}, {Type: 27, ID: 501, InvenIndex: 5050, Count: 9999}}},
		{AccountID: "2", OwnerIndex: 2, Version: 1, Snapshot: profile.Snapshot{OwnerIndex: 2}, RoomItems: []inventory.Item{{Type: 27, ID: 501, InvenIndex: 6060, Count: 9999}}},
	} {
		if err := f.Publish(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	seq := uint64(0)
	call := func(actor uint64, path string, fields []byte) ([]byte, error) {
		seq++
		_, body, err := f.Execute(ctx, command.Identity{AccountID: fmt.Sprint(actor), SessionID: "hidden-preset", RequestID: fmt.Sprint(seq)}, actor, path, append(wire.AppendVarint(nil, 1, seq), fields...), 0)
		return body, err
	}
	position := func(index, board, x uint64) []byte {
		return wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, index), 3, board), 4, x), 9, 1)
	}
	sourceRoom := wire.AppendBytes(wire.AppendBytes(wire.AppendVarint(nil, 1, 1), 3, position(1010, 2, 0)), 3, position(1010, 3, 0))
	if _, err := call(1, "/MyRoomSave", wire.AppendBytes(nil, 2, sourceRoom)); err != nil {
		t.Fatal(err)
	}
	preset := func(itemIndex, x uint64) []byte {
		item := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, itemIndex), 2, 501), 4, 2)
		room := wire.AppendBytes(wire.AppendBytes(wire.AppendVarint(nil, 1, 1), 3, position(itemIndex, 2, x)), 3, position(itemIndex, 3, 0))
		out := wire.AppendBytes(wire.AppendBytes(wire.AppendVarint(nil, 1, 1), 3, item), 4, room)
		return wire.AppendBytes(wire.AppendVarint(nil, 2, 1), 3, out)
	}
	if _, err := call(2, "/MyRoomPresetSave", preset(6060, 0)); err != nil {
		t.Fatalf("official hidden wallpaper substitution rejected: %v", err)
	}
	before, err := call(2, "/MyRoomPresetInfo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(2, "/MyRoomPresetSave", preset(6060, 1)); err == nil {
		t.Fatal("hidden wallpaper substitution allowed changed source coordinates")
	}
	if _, err := call(2, "/MyRoomPresetSave", preset(7070, 0)); err == nil {
		t.Fatal("hidden wallpaper substitution accepted unowned inventory index")
	}
	after, err := call(2, "/MyRoomPresetInfo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, after) {
		t.Fatal("invalid visited preset overwrote the valid stored layout")
	}
	found := false
	if err := wire.Walk(after, func(field wire.Field) error {
		if field.Number == 2 {
			found = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("visited preset was not stored in the visitor slots")
	}
}
