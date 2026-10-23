package room

import (
	"context"
	"encoding/binary"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type squareRules struct{ testRules }

func (squareRules) Profile(_ context.Context, p Principal) (Profile, error) {
	return Profile{UserInfo: text(integer(nil, 1, p.OwnerIndex), 4, p.AccountID), AvatarUseInfo: integer(integer(nil, 1, p.OwnerIndex), 2, 3), AvatarMotions: []uint64{7}, SquareUnlocked: p.AccountID != "locked"}, nil
}

func squareManager(t *testing.T, capacity int) (*Manager, *atomic.Int64) {
	t.Helper()
	clock := new(atomic.Int64)
	clock.Store(time.Now().UnixNano())
	m, err := New(Config{AdvertisedHost: "127.0.0.1", TCPPort: 9000, SquareChannels: 2, SquareChannelCapacity: capacity,
		Now: func() time.Time { return time.Unix(0, clock.Load()) }}, squareRules{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	return m, clock
}

// 2.37.13 NetworkContext requests 142, then 205, then relay 23 and 126.
// ENetMsg and CSSquare* generated fields define these independent wire expectations.
func squareRequest(t *testing.T, m *Manager, c Connection, typ int32, payload []byte) []Delivery {
	t.Helper()
	d, err := m.Handle(context.Background(), c, Frame{Type: typ, Sequence: 19, OwnerIndex: c.Principal.OwnerIndex, Payload: payload})
	if err != nil {
		t.Fatalf("native square packet %d: %v", typ, err)
	}
	return d
}

func squareJoin(t *testing.T, m *Manager, account string, owner int64) (Connection, string, string) {
	t.Helper()
	c := Connection{ID: "matching-" + account, Channel: Matching, Principal: Principal{AccountID: account, OwnerIndex: owner, SessionID: "session-" + account, Generation: 1}}
	d := squareRequest(t, m, c, 205, nil)
	if len(d) != 1 || d[0].Frame.Type != 206 || number(bytesField(d[0].Frame.Payload, 1), 2) != 0 {
		t.Fatalf("automatic channel assignment: %+v", d)
	}
	room, guid := stringField(d[0].Frame.Payload, 2), stringField(d[0].Frame.Payload, 6)
	if room == "" || guid == "" || stringField(d[0].Frame.Payload, 3) != "127.0.0.1" || stringField(d[0].Frame.Payload, 4) != "9000" {
		t.Fatal("channel assignment omitted the usable relay endpoint or membership GUID")
	}
	c.ID, c.Channel = "relay-"+account, Square
	squareRequest(t, m, c, 23, text(nil, 2, room))
	return c, room, guid
}

func squareEnter(t *testing.T, m *Manager, c Connection, room, guid string) []Delivery {
	t.Helper()
	state := blob(integer(nil, 1, c.Principal.OwnerIndex), 2, integer(nil, 1, 4))
	return squareRequest(t, m, c, 126, blob(text(text(nil, 2, guid), 3, room), 4, state))
}

func TestSquareNativeEntryPresenceAndStateAuthority(t *testing.T) {
	m, _ := squareManager(t, 2)
	a, code, ga := squareJoin(t, m, "alice", 11)
	initial := squareEnter(t, m, a, code, ga)
	if len(initial) != 1 || initial[0].Frame.Type != 127 || len(repeated(initial[0].Frame.Payload, 3)) != 1 {
		t.Fatalf("first entrant snapshot: %+v", initial)
	}
	b, otherCode, gb := squareJoin(t, m, "bob", 22)
	if otherCode != code || ga == gb {
		t.Fatal("available channel was not shared with independent membership GUIDs")
	}
	joined := squareEnter(t, m, b, code, gb)
	if len(joined) != 2 || joined[0].Frame.Type != 127 || len(repeated(joined[0].Frame.Payload, 3)) != 2 || joined[1].ConnectionID != a.ID || joined[1].Frame.Type != 128 {
		t.Fatalf("second entrant should see both players and announce arrival: %+v", joined)
	}
	for _, user := range repeated(joined[0].Frame.Payload, 3) {
		owner := number(bytesField(user, 1), 1)
		if number(bytesField(user, 3), 1) != owner || number(bytesField(user, 4), 1) != owner || number(bytesField(user, 4), 2) != 3 {
			t.Fatal("entry user/avatar identity did not come from committed profile")
		}
	}
	for _, payload := range [][]byte{
		blob(text(nil, 2, gb), 3, integer(nil, 1, 11)),
		blob(text(nil, 2, ga), 3, integer(nil, 1, 22)),
	} {
		if _, err := m.Handle(context.Background(), a, Frame{Type: 175, Payload: integer(payload, 4, 1)}); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("foreign membership/state owner accepted: %v", err)
		}
	}
	state := blob(integer(nil, 1, 11), 2, integer(nil, 1, 9))
	payload := integer(blob(text(nil, 2, ga), 3, state), 4, 1)
	d := squareRequest(t, m, a, 175, payload)
	if len(d) != 1 || d[0].ConnectionID != b.ID || d[0].Frame.Type != 176 || number(bytesField(bytesField(d[0].Frame.Payload, 2), 2), 1) != 9 {
		t.Fatalf("authorized movement was not relayed to the other player: %+v", d)
	}
	quiet := squareRequest(t, m, a, 175, blob(text(nil, 2, ga), 3, state))
	if len(quiet) != 0 {
		t.Fatal("IsBroadCast=false movement was broadcast")
	}
	matching := a
	matching.Channel, matching.ID = Matching, "matching-alice"
	info := squareRequest(t, m, matching, 142, nil)
	if len(info) != 1 || info[0].Frame.Type != 143 || number(info[0].Frame.Payload, 3) != 2 || number(bytesField(info[0].Frame.Payload, 2), 2) != 2 {
		t.Fatalf("directory did not report live channel occupancy: %+v", info)
	}
}

func TestSquareCapacityChannelTransferAndReconnectFencing(t *testing.T) {
	m, clock := squareManager(t, 1)
	a, roomA, ga := squareJoin(t, m, "alice", 11)
	squareEnter(t, m, a, roomA, ga)
	b, roomB, gb := squareJoin(t, m, "bob", 22)
	if roomA == roomB {
		t.Fatal("automatic entry overbooked a full channel")
	}
	squareEnter(t, m, b, roomB, gb)
	matching := a
	matching.ID, matching.Channel = "matching-alice", Matching
	full := squareRequest(t, m, matching, 124, integer(nil, 2, 2))
	if number(bytesField(full[0].Frame.Payload, 1), 2) != 11 {
		t.Fatal("full channel did not return E_Matching_Room_Over_User_Count")
	}
	if view, err := m.View(context.Background(), a.Principal, Square); err != nil || view.RoomCode != roomA {
		t.Fatal("failed channel transfer discarded the existing membership")
	}
	exit := squareRequest(t, m, b, 129, text(nil, 2, gb))
	if len(exit) != 1 || exit[0].Frame.Type != 130 {
		t.Fatal("explicit square exit did not acknowledge")
	}
	transfer := squareRequest(t, m, matching, 124, integer(nil, 2, 2))
	newCode, newGUID := stringField(transfer[0].Frame.Payload, 2), stringField(transfer[0].Frame.Payload, 6)
	if newCode != roomB || newGUID == ga {
		t.Fatal("channel transfer did not issue new room membership")
	}
	if _, err := m.Handle(context.Background(), a, Frame{Type: 175, Payload: text(nil, 2, ga)}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old room socket retained authority after channel transfer: %v", err)
	}
	a.ID = "relay-alice-channel2"
	squareRequest(t, m, a, 23, text(nil, 2, newCode))
	squareEnter(t, m, a, newCode, newGUID)
	if _, err := m.Disconnect(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	replacement := a
	replacement.ID = "relay-alice-reconnected"
	d := squareRequest(t, m, replacement, 185, text(nil, 2, newGUID))
	if len(d) != 1 || d[0].Frame.Type != 186 || len(repeated(d[0].Frame.Payload, 5)) != 1 {
		t.Fatal("reconnect did not restore the player's committed room snapshot")
	}
	if _, err := m.Handle(context.Background(), a, Frame{Type: 129, Payload: text(nil, 2, newGUID)}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("replaced relay socket retained authority: %v", err)
	}
	if _, err := m.Disconnect(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	clock.Add(int64(16 * time.Second))
	if reply, err := m.Handle(context.Background(), replacement, Frame{Type: 185, Payload: text(nil, 2, newGUID)}); err != nil || len(reply) != 1 || (number(bytesField(reply[0].Frame.Payload, 1), 2) != 10 && number(bytesField(reply[0].Frame.Payload, 1), 2) != 3) {
		t.Fatalf("expired reconnect did not return native failure: %+v %v", reply, err)
	}
}

func TestSquareRelayRejectsSpoofedIdentityAndForeignGameTraffic(t *testing.T) {
	m, _ := squareManager(t, 2)
	a, code, ga := squareJoin(t, m, "alice", 11)
	squareEnter(t, m, a, code, ga)
	b, _, gb := squareJoin(t, m, "bob", 22)
	squareEnter(t, m, b, code, gb)
	relay := func(owner int64, typ uint32) []byte {
		inner := make([]byte, 4)
		binary.LittleEndian.PutUint32(inner, typ)
		return blob(text(nil, 2, ga), 3, append(inner, integer(nil, 1, owner)...))
	}
	for _, payload := range [][]byte{relay(22, 9005), relay(11, 1011)} {
		if _, err := m.Handle(context.Background(), a, Frame{Type: 70, Payload: payload}); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("spoofed user/foreign gameplay relay accepted: %v", err)
		}
	}
	d := squareRequest(t, m, a, 70, relay(11, 9005))
	if len(d) != 1 || d[0].Frame.Type != 71 || d[0].ConnectionID != b.ID || number(d[0].Frame.Payload, 2) != 11 {
		t.Fatalf("native UserStateRelayData not delivered: %+v", d)
	}
	outsider := a
	outsider.Principal.AccountID = "outsider"
	if reply, err := m.Handle(context.Background(), outsider, Frame{Type: 126, Payload: text(text(nil, 2, ga), 3, code)}); err != nil || len(reply) != 1 || number(bytesField(reply[0].Frame.Payload, 1), 2) != 3 {
		t.Fatalf("nonmember did not receive E_Not_Found_Room: %+v %v", reply, err)
	}
}

func TestSquareNewPlayerGenerationReplacesExpiredLoginMembership(t *testing.T) {
	m, _ := squareManager(t, 1)
	old, code, guid := squareJoin(t, m, "alice", 11)
	squareEnter(t, m, old, code, guid)
	current := old
	current.ID, current.Channel = "new-login-matching", Matching
	current.Principal.Generation++
	current.Principal.SessionID = "new-login-session"
	d := squareRequest(t, m, current, 205, nil)
	if len(d) != 1 || stringField(d[0].Frame.Payload, 6) == guid || stringField(d[0].Frame.Payload, 2) != code {
		t.Fatal("new login did not replace its previous square membership")
	}
	if _, err := m.View(context.Background(), old.Principal, Square); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old player generation retained membership: %v", err)
	}
	old.Channel, old.ID = Matching, "old-login-matching"
	if _, err := m.Handle(context.Background(), old, Frame{Type: 205}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("retired generation reclaimed membership: %v", err)
	}
}

func TestSquareAvatarMotionUsesOwnedProfile(t *testing.T) {
	m, _ := squareManager(t, 2)
	a, code, ga := squareJoin(t, m, "alice", 11)
	squareEnter(t, m, a, code, ga)
	b, _, gb := squareJoin(t, m, "bob", 22)
	squareEnter(t, m, b, code, gb)
	if _, err := m.Handle(context.Background(), a, Frame{Type: 144, Payload: blob(nil, 2, integer(nil, 1, 99))}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unowned avatar motion accepted: %v", err)
	}
	d := squareRequest(t, m, a, 144, blob(nil, 2, integer(nil, 1, 7)))
	if len(d) != 2 || d[0].Frame.Type != 145 || d[1].Frame.Type != 146 || d[1].ConnectionID != b.ID || number(bytesField(d[1].Frame.Payload, 3), 1) != 7 {
		t.Fatalf("owned avatar motion not acknowledged/broadcast: %+v", d)
	}
}

func TestSquareLockedPlayerCanReadDirectoryButCannotReserveChannel(t *testing.T) {
	m, _ := squareManager(t, 2)
	c := Connection{ID: "locked-matching", Channel: Matching, Principal: Principal{AccountID: "locked", OwnerIndex: 33, SessionID: "session-locked", Generation: 1}}
	d := squareRequest(t, m, c, 142, nil)
	if len(d) != 1 || d[0].Frame.Type != 143 || number(d[0].Frame.Payload, 3) != 2 {
		t.Fatal("public channel discovery incorrectly requires card ownership")
	}
	for _, req := range []Frame{{Type: 205}, {Type: 124, Payload: integer(nil, 2, 1)}} {
		if _, err := m.Handle(context.Background(), c, req); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("locked player acquired square channel via %d: %v", req.Type, err)
		}
	}
	if _, err := m.View(context.Background(), c.Principal, Square); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected reservation left membership: %v", err)
	}
}
