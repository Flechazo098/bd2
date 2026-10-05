package commerce

import (
	"bytes"
	"testing"
	"time"

	"bd2server/internal/server/events"
	"bd2server/internal/server/eventtasks"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
)

type originalAttendance struct{}

func (originalAttendance) Handle(path string, _ []byte) (int, []byte, bool, error) {
	return 0, wire.AppendVarint(nil, 55, 123), path == "/Attendance", nil
}

type attendanceBalances struct {
	balance uint64
	calls   int
}

func (e *attendanceBalances) Apply(_ string, _ []gamedata.Reward, rewards []gamedata.Reward) ([]byte, error) {
	e.calls++
	var bundle []byte
	for _, r := range rewards {
		e.balance += r.Count
		bundle = wire.AppendBytes(bundle, 1, player.ItemWire(player.Item{Type: r.Type, ID: r.ID, Count: r.Count}))
	}
	return bundle, nil
}

func TestAttendanceCombinesOrdinaryLoginPassAndSubscriptionInGrantOrder(t *testing.T) {
	e, items, _, _, now := entitlementFixture(t)
	// eventtasks uses the production clock; align the commerce fixture with it
	// while buying the subscription on the previous reset day.
	*now = time.Now().UTC().Add(-24 * time.Hour)
	balances := &attendanceBalances{}
	e.base = balances
	if _, err := e.Apply("subscription", nil, []gamedata.Reward{{Type: 19, ID: 38, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(24 * time.Hour)
	registry := events.NewRegistry()
	if err := registry.Replace([]events.Schedule{{UID: 17, Type: 0, ID: 1, Start: 1, End: now.Add(time.Hour).UnixMilli()}}); err != nil {
		t.Fatal(err)
	}
	design := &gamedata.EventTasksDesign{
		Attendance:        map[uint64]gamedata.EventAttendance{1: {ID: 1, Group: 10}},
		AttendanceRewards: map[uint64][]gamedata.EventAttendanceReward{10: {{Group: 10, ID: 1, Day: 1, Basic: gamedata.Reward{Type: 3, Count: 2}}}},
	}
	tasks, err := eventtasks.Open(e.store, design, registry, e)
	if err != nil {
		t.Fatal(err)
	}
	tasks.SetSession("combined-session")
	passes, err := NewLoginPasses(e.store, &gamedata.LoginPassCatalog{Groups: map[uint64][]gamedata.LoginPassReward{
		20: {{ID: 1, TicketID: 77, Free: gamedata.Reward{Type: 3, Count: 11}}},
	}}, e, items, func(uint64) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	passes.SetClock(func() time.Time { return *now }, 0)
	h := AttendanceHandler{Events: tasks, Economy: e, LoginPasses: passes, Store: e.store}
	request := wire.AppendVarint(nil, 1, 1)
	_, response, _, err := h.HandleSession("/Attendance", request, "combined-session")
	if err != nil {
		t.Fatal(err)
	}
	if balances.calls != 4 || balances.balance != 19 {
		t.Fatalf("wrong combined grant: calls=%d balance=%d", balances.calls, balances.balance)
	}
	counts := map[int]int{}
	if err := wire.Walk(response, func(f wire.Field) error { counts[f.Number]++; return nil }); err != nil {
		t.Fatal(err)
	}
	if counts[1001] != 1 || counts[1002] != 1 || counts[5] != 1 || counts[6] != 1 {
		t.Fatalf("client requires a single combined envelope and native notices: %v", counts)
	}
	bundle, _, _ := wire.Bytes(response, 1001)
	var snapshots []uint64
	if err := wire.Walk(bundle, func(f wire.Field) error {
		if f.Number == 1 {
			count, _, err := wire.Varint(f.Value, 4)
			if err != nil {
				return err
			}
			snapshots = append(snapshots, count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 3 || snapshots[0] != 2 || snapshots[1] != 11 || snapshots[2] != 5 {
		t.Fatalf("combined reward entries lost or reordered: %v", snapshots)
	}
	_, replay, _, err := h.HandleSession("/Attendance", request, "combined-session")
	if err != nil || !bytes.Equal(response, replay) || balances.calls != 4 {
		t.Fatal("combined retry changed rewards", err)
	}
	_, next, _, err := h.HandleSession("/Attendance", wire.AppendVarint(nil, 1, 2), "combined-session")
	if err != nil || balances.calls != 4 {
		t.Fatal("fresh request granted again", err)
	}
	if _, ok, _ := wire.Bytes(next, 1001); ok {
		t.Fatal("fresh request replays old reward envelope")
	}
	if _, ok, _ := wire.Bytes(next, 5); ok {
		t.Fatal("fresh request replays old attendance stamps")
	}
}

func TestAttendanceRejectsAmbiguousChildRewardEnvelope(t *testing.T) {
	native := wire.AppendVarint(nil, 55, 123)
	bundle := wire.AppendBytes(native, 1001, wire.AppendVarint(nil, 7, 1))
	valid := wire.AppendString(bundle, 1002, "event-receipt")
	stripped, rewards, err := takeAttendanceRewardEnvelope(valid)
	if err != nil || !bytes.Equal(stripped, native) || len(rewards) == 0 {
		t.Fatal("valid envelope was not preserved", err)
	}
	for _, malformed := range [][]byte{
		bundle,
		wire.AppendString(native, 1002, "event-receipt"),
		wire.AppendBytes(valid, 1001, nil),
		wire.AppendString(valid, 1002, "second"),
		wire.AppendString(bundle, 1002, ""),
		wire.AppendVarint(native, 1001, 1),
	} {
		if _, _, err := takeAttendanceRewardEnvelope(malformed); err == nil {
			t.Fatalf("ambiguous envelope accepted: %x", malformed)
		}
	}
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
