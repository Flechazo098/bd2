package commerce

import (
	"bytes"
	"testing"
	"time"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
)

type originalAttendance struct{}

func (originalAttendance) Handle(path string, _ []byte) (int, []byte, bool, error) {
	return 0, wire.AppendVarint(nil, 55, 123), path == "/Attendance", nil
}

func TestAttendanceExtensionReplaysRewardAndPreservesNativeResponse(t *testing.T) {
	e, _, base, _, now := entitlementFixture(t)
	if _, err := e.Apply("subscription", nil, []gamedata.Reward{{Type: 19, ID: 38, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(24 * time.Hour)
	h := AttendanceHandler{Events: originalAttendance{}, Economy: e, Store: e.store}
	request := wire.AppendVarint(nil, 1, 1)
	_, response, handled, err := h.HandleSession("/Attendance", request, "session")
	if err != nil || !handled {
		t.Fatal(err)
	}
	if v, ok, _ := wire.Varint(response, 55); !ok || v != 123 {
		t.Fatal("native response lost")
	}
	bundle, ok, err := wire.Bytes(response, 1001)
	if err != nil || !ok || len(bundle) == 0 {
		t.Fatal("reward extension missing")
	}
	if receipt, ok, _ := wire.Bytes(response, 1002); !ok || len(receipt) == 0 {
		t.Fatal("receipt extension missing")
	}
	calls := base.Calls
	_, replay, _, err := h.HandleSession("/Attendance", request, "session")
	if err != nil || !bytes.Equal(response, replay) || base.Calls != calls {
		t.Fatal("retry lost reward or granted twice", err)
	}
	_, next, _, err := h.HandleSession("/Attendance", wire.AppendVarint(nil, 1, 2), "session")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := wire.Bytes(next, 1001); ok || base.Calls != calls {
		t.Fatal("same day duplicate grant")
	}
	_, _, _, err = h.HandleSession("/Attendance", append(request, wire.AppendVarint(nil, 2, 1)...), "session")
	if err == nil {
		t.Fatal("conflicting sequence accepted")
	}
}

func TestAttendanceCombinesLoginPassAndSubscriptionWithoutDuplicateClaims(t *testing.T) {
	e, items, base, _, now := entitlementFixture(t)
	if _, err := e.Apply("subscription", nil, []gamedata.Reward{{Type: 19, ID: 38, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(24 * time.Hour)
	design := &gamedata.LoginPassCatalog{Groups: map[uint64][]gamedata.LoginPassReward{
		10: {{ID: 1, TicketID: 77, Free: gamedata.Reward{Type: 3, Count: 11}, Premium: gamedata.Reward{Type: 3, Count: 22}}},
	}}
	passes, err := NewLoginPasses(e.store, design, e, items, func(group uint64) bool { return group == 10 })
	if err != nil {
		t.Fatal(err)
	}
	passes.SetClock(func() time.Time { return *now }, 0)
	h := AttendanceHandler{Events: originalAttendance{}, Economy: e, LoginPasses: passes, Store: e.store}
	calls := base.Calls
	request := wire.AppendVarint(nil, 1, 1)
	_, response, _, err := h.HandleSession("/Attendance", request, "combined-session")
	if err != nil || base.Calls != calls+2 {
		t.Fatalf("daily claims not combined: calls=%d err=%v", base.Calls-calls, err)
	}
	info, ok, err := wire.Bytes(response, 6)
	if err != nil || !ok {
		t.Fatal("login-pass metadata lost", err)
	}
	if group, _, _ := wire.Varint(info, 1); group != 10 {
		t.Fatal("wrong login-pass group")
	}
	bundle, ok, err := wire.Bytes(response, 1001)
	if err != nil || !ok || len(bundle) == 0 {
		t.Fatal("combined reward envelope missing", err)
	}
	_, replay, _, err := h.HandleSession("/Attendance", request, "combined-session")
	if err != nil || !bytes.Equal(replay, response) || base.Calls != calls+2 {
		t.Fatal("retry changed the envelope or repeated claims", err)
	}
	_, next, _, err := h.HandleSession("/Attendance", wire.AppendVarint(nil, 1, 2), "combined-session")
	if err != nil || base.Calls != calls+2 {
		t.Fatal("fresh request claimed the same day again", err)
	}
	if _, ok, _ := wire.Bytes(next, 1001); ok {
		t.Fatal("fresh request replayed old rewards")
	}
}
