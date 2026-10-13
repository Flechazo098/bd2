//go:build integration

package app

import (
	"context"
	"slices"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func TestNativeRoomVisitPrivacyOwnedLayoutAndPopularityAreDurable(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	actor := openNicknamePlayer(t, factory, accounts[1])
	tx, err := actor.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := actor.assembly.collection.GrantCostumes(command.Context{State: tx}, "room-costume-fixture", []uint64{101}, factory.design.rewardCostumes); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	items, err := actor.assembly.ownedItems.GrantOnce(command.Context{State: tx}, "room-decoration-fixture", []gamedata.BattleReward{{Type: 27, ID: 101, Count: 1}})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if len(items) != 1 {
		_ = tx.Rollback()
		t.Fatal("fixture decoration not granted")
	}
	decorationIndex := items[0].InvenIndex
	var costumeIndex uint64
	for _, costume := range actor.assembly.collection.Costumes() {
		if costume.ID == 101 {
			costumeIndex = costume.InvenIndex
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	closeNicknamePlayer(t, actor)
	if costumeIndex == 0 {
		t.Fatal("fixture has no owned costume")
	}
	var shared *sharedRuntime
	var registry *playerRegistry
	open := func() {
		var err error
		shared, err = openSharedRuntime(context.Background(), factory.options, factory.design)
		if err != nil {
			t.Fatal(err)
		}
		factory.shared, factory.pvpSource = shared, shared.pvp
		registry = newPlayerRegistry(factory, time.Minute)
	}
	close := func() {
		if registry != nil {
			if err := registry.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			registry = nil
		}
		if shared != nil {
			if err := shared.store.Close(); err != nil {
				t.Fatal(err)
			}
			shared = nil
		}
	}
	t.Cleanup(close)
	open()
	a, b := friendHTTPLogin(t, registry, accounts[0]), friendHTTPLogin(t, registry, accounts[1])
	drainFriendPublications(t, registry, shared, accounts[0])
	drainFriendPublications(t, registry, shared, accounts[1])
	self := appRows(b.send("/MyRoomUserInfo", append(appScalar(2, b.owner), appScalar(3, 1)...), 228, 0), 1)
	if len(self) != 1 || appValue(self[0], 1) != b.owner {
		t.Fatal("own room view omitted actual owner")
	}
	rooms := appRows(self[0], 8)
	// Read-only current MyRoomExpandTable1/2/3 each has field3(defaultRoom)=1.
	if len(rooms) != 3 {
		t.Fatalf("native default rooms=%d want3", len(rooms))
	}
	primary := appValue(self[0], 4)
	position := wire.AppendVarint(nil, 1, costumeIndex)
	position = wire.AppendVarint(position, 2, 1)
	position = wire.AppendVarint(position, 3, 1)
	room := wire.AppendVarint(nil, 1, primary)
	room = wire.AppendString(room, 2, "Quiet Room")
	room = wire.AppendBytes(room, 3, position)
	decorationPosition := wire.AppendVarint(nil, 1, decorationIndex)
	decorationPosition = wire.AppendVarint(decorationPosition, 3, 1)
	room = wire.AppendBytes(room, 3, decorationPosition)
	b.send("/MyRoomSave", appMessage(2, room), 229, 0)
	foreign := wire.AppendVarint(nil, 1, primary)
	foreignPosition := wire.AppendVarint(nil, 1, 9223372036854775807)
	foreignPosition = wire.AppendVarint(foreignPosition, 2, 1)
	foreignPosition = wire.AppendVarint(foreignPosition, 3, 1)
	foreign = wire.AppendBytes(foreign, 3, foreignPosition)
	b.send("/MyRoomSave", appMessage(2, foreign), 229, 9003)
	view := appRows(a.send("/MyRoomUserInfo", appScalar(2, b.owner), 228, 0), 1)[0]
	if appValue(view, 1) != b.owner || len(appRows(view, 6)) == 0 || len(appRows(view, 8)) != 3 {
		t.Fatal("visitor did not receive target's actual costumes and visible default rooms")
	}
	var chosen []byte
	for _, v := range appRows(view, 8) {
		if appValue(v, 1) == primary {
			chosen = v
		}
	}
	hasCostumePlacement := false
	for _, placement := range appRows(chosen, 3) {
		if appValue(placement, 1) == costumeIndex && appValue(placement, 2) == 1 {
			hasCostumePlacement = true
		}
	}
	if nicknameField(t, chosen, 2) != "Quiet Room" || !hasCostumePlacement {
		t.Fatal("visitor layout did not match owner's persisted save")
	}
	profile := a.send("/UserContentsInfo", appScalar(2, b.owner), 215, 0)
	if len(appRows(appRows(profile, 14)[0], 8)) != 1 {
		t.Fatal("profile click omitted representative owned room")
	}
	like := a.send("/MyRoomLike", appScalar(2, b.owner), 420, 0)
	if appValue(like, 1) != 1 || appValue(like, 2) != 1 || appValue(like, 3) == 0 {
		t.Fatal("monthly popularity omitted month,total or viewer date")
	}
	a.seq--
	if replay := a.send("/MyRoomLike", appScalar(2, b.owner), 420, 0); !slices.Equal(like, replay) {
		t.Fatal("popularity retry changed response")
	}
	a.send("/MyRoomLike", appScalar(2, b.owner), 420, 9003)
	b.send("/MyRoomLike", appScalar(2, b.owner), 420, 9003)
	if appValue(a.send("/UserContentsInfo", appScalar(2, b.owner), 215, 0), 18) != 1 {
		t.Fatal("profile room popularity was not projected")
	}
	if rows := appRows(a.send("/MyRoomSearchRecommend", appScalar(2, 1), 231, 0), 1); len(rows) != 1 || appValue(rows[0], 1) != b.owner {
		t.Fatal("popular visitor list omitted liked owner")
	}
	if rows := appRows(a.send("/MyRoomSearchRecommend", nil, 231, 0), 1); len(rows) != 1 || appValue(rows[0], 1) != b.owner {
		t.Fatal("random visitor list omitted populated liked room")
	}
	templateItem := wire.AppendVarint(nil, 1, decorationIndex)
	templateItem = wire.AppendVarint(templateItem, 2, 101)
	templateItem = wire.AppendVarint(templateItem, 4, 1)
	templateRoom := wire.AppendVarint(nil, 1, primary)
	templateRoom = wire.AppendBytes(templateRoom, 3, decorationPosition)
	template := wire.AppendVarint(nil, 1, 1)
	template = wire.AppendString(template, 2, "Visited Layout")
	template = wire.AppendBytes(template, 3, templateItem)
	template = wire.AppendBytes(template, 4, templateRoom)
	templateRequest := wire.AppendVarint(nil, 2, b.owner)
	templateRequest = wire.AppendBytes(templateRequest, 3, template)
	callerBeforeCopy := appRows(a.send("/MyRoomUserInfo", appScalar(2, a.owner), 228, 0), 1)[0]
	itemsBeforeCopy := appRows(callerBeforeCopy, 5)
	a.send("/MyRoomPresetSave", templateRequest, 642, 0)
	copied := appRows(a.send("/MyRoomPresetInfo", nil, 641, 0), 2)
	if len(copied) != 1 || appValue(copied[0], 1) != 1 || nicknameField(t, copied[0], 2) != "Visited Layout" || appValue(appRows(appRows(copied[0], 4)[0], 3)[0], 1) != decorationIndex {
		t.Fatal("visitor template did not retain exact source layout")
	}
	ownAfterCopy := appRows(a.send("/MyRoomUserInfo", appScalar(2, a.owner), 228, 0), 1)[0]
	itemsAfterCopy := appRows(ownAfterCopy, 5)
	if !slices.EqualFunc(itemsBeforeCopy, itemsAfterCopy, func(a, b []byte) bool { return slices.Equal(a, b) }) {
		t.Fatal("saving visitor template granted source furnishings to caller")
	}
	b.send("/MyRoomShareOptionUpdate", appScalar(2, 2), 645, 0)
	a.send("/MyRoomUserInfo", appScalar(2, b.owner), 228, 9003)
	a.send("/MyRoomPresetSave", templateRequest, 642, 9003)
	if rows := appRows(a.send("/MyRoomSearchRecommend", appScalar(2, 1), 231, 0), 1); len(rows) != 0 {
		t.Fatal("private room leaked into popular visits")
	}
	a.send("/FriendSend", appScalar(2, b.owner), 205, 0)
	b.send("/FriendAccept", appScalar(2, a.owner), 206, 0)
	if rows := appRows(a.send("/MyRoomSearchFriend", nil, 230, 0), 1); len(rows) != 1 {
		t.Fatal("accepted friend could not visit friend-only room")
	}
	hidden := wire.AppendVarint(room, 4, 1)
	b.send("/MyRoomSave", appMessage(2, hidden), 229, 0)
	a.send("/MyRoomUserInfo", appScalar(2, b.owner), 228, 9003)
	if rows := appRows(a.send("/MyRoomSearchFriend", nil, 230, 0), 1); len(rows) != 0 {
		t.Fatal("hidden representative room remained in friend visit list")
	}
	b.send("/MyRoomSave", appMessage(2, room), 229, 0)
	close()
	open()
	a, b = friendHTTPLogin(t, registry, accounts[0]), friendHTTPLogin(t, registry, accounts[1])
	drainFriendPublications(t, registry, shared, accounts[0])
	drainFriendPublications(t, registry, shared, accounts[1])
	view = appRows(a.send("/MyRoomUserInfo", appScalar(2, b.owner), 228, 0), 1)[0]
	if appValue(view, 9) != 1 || appValue(view, 10) == 0 || appValue(view, 12) != 2 {
		t.Fatal("reopen lost popularity, viewer timestamp or share scope")
	}
	copied = appRows(a.send("/MyRoomPresetInfo", nil, 641, 0), 2)
	if len(copied) != 1 || nicknameField(t, copied[0], 2) != "Visited Layout" {
		t.Fatal("visitor template lost across database reopen")
	}
	a.send("/MyRoomPresetNameChange", append(append(appScalar(2, 2), appScalar(3, 1)...), appMessage(4, []byte("Saved Visit"))...), 643, 0)
	copied = appRows(a.send("/MyRoomPresetInfo", nil, 641, 0), 2)
	if len(copied) != 1 || nicknameField(t, copied[0], 2) != "Saved Visit" {
		t.Fatal("template rename did not persist")
	}
	a.send("/MyRoomPresetDelete", append(appScalar(2, 2), appScalar(3, 1)...), 644, 0)
	if len(appRows(a.send("/MyRoomPresetInfo", nil, 641, 0), 2)) != 0 {
		t.Fatal("deleted template remained in visitor saved list")
	}
	b.send("/MyRoomNameChange", append(appScalar(2, primary), appMessage(3, []byte("Renamed Room"))...), 234, 0)
	view = appRows(a.send("/MyRoomUserInfo", appScalar(2, b.owner), 228, 0), 1)[0]
	var renamed []byte
	for _, v := range appRows(view, 8) {
		if appValue(v, 1) == primary {
			renamed = v
		}
	}
	if len(renamed) == 0 || nicknameField(t, renamed, 2) != "Renamed Room" {
		t.Fatal("room name change was not visible to friend")
	}
	a.send("/FriendRemove", appScalar(2, b.owner), 208, 0)
	a.send("/MyRoomUserInfo", appScalar(2, b.owner), 228, 9003)
	privateProfile := a.send("/UserContentsInfo", appScalar(2, b.owner), 215, 0)
	if len(appRows(appRows(privateProfile, 14)[0], 8)) != 0 {
		t.Fatal("profile click bypassed room privacy after friend removal")
	}
}
