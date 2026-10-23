//go:build integration

package app

import (
	"context"
	"testing"
	"time"
)

// FriendDBInfo field8 and GuildMemberDBInfo field10 use the same last-login
// time. Presence is a response projection; publishing a future display time
// must never overwrite the actual persisted login time.
func TestFriendPresenceChangesWithoutChangingStoredLoginTime(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	shared, err := openSharedRuntime(context.Background(), factory.options, factory.design)
	if err != nil {
		t.Fatal(err)
	}
	factory.shared, factory.pvpSource = shared, shared.pvp
	registry := newPlayerRegistry(factory, time.Minute)
	t.Cleanup(func() {
		if err := registry.Close(context.Background()); err != nil {
			t.Error(err)
		}
		if err := shared.store.Close(); err != nil {
			t.Error(err)
		}
	})
	a, b := friendHTTPLogin(t, registry, accounts[0]), friendHTTPLogin(t, registry, accounts[1])
	drainFriendPublications(t, registry, shared, accounts[0])
	drainFriendPublications(t, registry, shared, accounts[1])
	a.send("/FriendSend", appScalar(2, b.owner), 205, 0)
	b.send("/FriendAccept", appScalar(2, a.owner), 206, 0)
	stored, err := shared.friends.Profile(context.Background(), b.owner)
	if err != nil {
		t.Fatal(err)
	}
	online := true
	shared.friends.AttachPresence(func(account string) bool { return online && account == accounts[1] })
	rows := appRows(a.send("/FriendInfoList", nil, 204, 0), 1)
	if len(rows) != 1 || appValue(rows[0], 1) != b.owner || int64(appValue(rows[0], 8)) <= time.Now().UnixMilli() {
		t.Fatal("online friend did not receive native online display marker")
	}
	persisted, err := shared.friends.Profile(context.Background(), b.owner)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.LastLoginMS != stored.LastLoginMS {
		t.Fatal("online projection overwrote actual last-login time")
	}
	online = false
	rows = appRows(a.send("/FriendInfoList", nil, 204, 0), 1)
	if len(rows) != 1 || int64(appValue(rows[0], 8)) != stored.LastLoginMS {
		t.Fatal("offline friend retained online marker or lost last-login time")
	}
}
