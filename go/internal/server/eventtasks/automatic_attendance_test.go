package eventtasks

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"errors"
	"testing"
	"time"
)

func claimEntries(b []byte) int {
	n := 0
	_ = wire.Walk(b, func(f wire.Field) error {
		if f.Number == 5 {
			n++
		}
		return nil
	})
	return n
}
func TestAutomaticAttendanceLoginMarkerReplayAndRestart(t *testing.T) {
	s, eco, store := setup(t)
	m := s.attendanceMail.(*attendanceMailStub)
	if _, b, _, err := s.Handle("/LoginEvent", req(1)); err != nil || claimEntries(b) != 0 || eco.calls != 0 {
		t.Fatal("login event silently granted", err)
	}
	_, response, _, err := s.Handle("/Attendance", req(2))
	if err != nil || m.calls != 1 || eco.calls != 0 || claimEntries(response) != 1 || s.state.Attendance["1"].Count != 1 {
		t.Fatal("shared marker prevented actual grant", err)
	}
	reopened, err := Open(store, s.design, s.registry, eco)
	if err != nil {
		t.Fatal(err)
	}
	reopened.AttachAttendanceMail(m)
	reopened.now = s.now
	reopened.SetSession("test")
	_, replay, _, err := reopened.Handle("/Attendance", req(2))
	if err != nil || !bytes.Equal(response, replay) || m.calls != 1 || eco.calls != 0 {
		t.Fatal("durable replay changed", err)
	}
	reopened.SetSession("reconnect")
	_, response, _, err = reopened.Handle("/Attendance", req(1))
	if err != nil || claimEntries(response) != 0 || m.calls != 1 || eco.calls != 0 {
		t.Fatal("history returned as new reward", err)
	}
	if _, present, _ := wire.Bytes(response, 1001); present {
		t.Fatal("empty grant had bundle")
	}
}
func TestAutomaticLimitAttendanceUsesScheduleDateAndSkipsInactive(t *testing.T) {
	s, eco, _ := setup(t)
	m := s.attendanceMail.(*attendanceMailStub)
	now := s.now()
	reg := events.NewRegistry()
	rows := []events.Schedule{
		{UID: 10, Type: 1, ID: 7, Start: now.Add(-48 * time.Hour).UnixMilli(), End: now.Add(48 * time.Hour).UnixMilli()},
		{UID: 11, Type: 1, ID: 8, Start: now.Add(time.Hour).UnixMilli(), End: now.Add(48 * time.Hour).UnixMilli()},
		{UID: 12, Type: 1, ID: 9, Start: now.Add(-48 * time.Hour).UnixMilli(), End: now.UnixMilli()},
	}
	if err := reg.Replace(rows); err != nil {
		t.Fatal(err)
	}
	s.registry = reg
	s.design.LimitRewards = map[[2]uint64]uint64{{7, 1}: 100, {7, 2}: 200, {7, 3}: 300, {7, 4}: 400, {8, 1}: 500, {9, 3}: 600}
	s.state.LoginDays[now.Add(-24*time.Hour).Format("2006-01-02")] = now.Add(-24 * time.Hour).UnixMilli()
	_, response, _, err := s.Handle("/Attendance", req(1))
	if err != nil || claimEntries(response) != 1 || m.calls != 1 || eco.calls != 0 || len(m.rewards) != 1 || m.rewards[0].ID != 300 {
		t.Fatal("wrong calendar day granted", m.rewards, err)
	}
	if len(s.state.Attendance) != 1 {
		t.Fatal("inactive calendar mutated")
	}
	now = now.Add(24 * time.Hour)
	s.now = func() time.Time { return now }
	_, response, _, err = s.Handle("/Attendance", req(2))
	if err != nil || claimEntries(response) != 1 || m.rewards[len(m.rewards)-1].ID != 400 {
		t.Fatal("next calendar day missing", err)
	}
}
func TestAutomaticAttendanceUIDZeroSeparateDesignsAndSingleSettlement(t *testing.T) {
	s, eco, _ := setup(t)
	m := s.attendanceMail.(*attendanceMailStub)
	now := s.now()
	reg := events.NewRegistry()
	if err := reg.Replace([]events.Schedule{{Type: 0, ID: 1, Start: now.Add(-time.Hour).UnixMilli(), End: now.Add(time.Hour).UnixMilli()}, {Type: 0, ID: 2, Start: now.Add(-time.Hour).UnixMilli(), End: now.Add(time.Hour).UnixMilli()}}); err != nil {
		t.Fatal(err)
	}
	s.registry = reg
	s.design.Attendance[2] = gamedata.EventAttendance{ID: 2, Group: 2}
	s.design.AttendanceRewards[2] = []gamedata.EventAttendanceReward{{ID: 1, Group: 2, Day: 1, Basic: gamedata.Reward{Type: 4, Count: 20}}, {ID: 2, Group: 2, Day: 2, Basic: gamedata.Reward{Type: 4, Count: 30}}}
	_, response, _, err := s.Handle("/Attendance", req(1))
	if err != nil || m.calls != 1 || eco.calls != 0 || len(m.rewards) != 2 || claimEntries(response) != 2 || len(s.state.Attendance) != 2 {
		t.Fatal("public zero identities collided", err)
	}
}
func TestAutomaticAttendanceMailFailureRestoresProgressAndClaims(t *testing.T) {
	s, eco, _ := setup(t)
	m := s.attendanceMail.(*attendanceMailStub)
	m.err = errors.New("settlement failure")
	if _, _, _, err := s.Handle("/Attendance", req(1)); err == nil {
		t.Fatal("failed settlement accepted")
	}
	if len(s.state.Attendance) != 0 || len(s.state.LoginDays) != 0 || len(s.state.Receipts) != 0 {
		t.Fatal("failed settlement persisted markers")
	}
	m.err = nil
	_, response, _, err := s.Handle("/Attendance", req(1))
	if err != nil || eco.calls != 0 || m.calls != 1 || claimEntries(response) != 1 || !s.state.Attendance["1"].Obtained["1/1"] {
		t.Fatal("retry lost eligibility", err)
	}
}

// The account transaction restores mailbox state; this store exercises the
// domain restoration that must also drop newly inserted map entries.
type attendanceFailStore struct{ stateio.Store }

func (s attendanceFailStore) Save(string, []byte) error { return errors.New("save failure") }
func TestAutomaticAttendanceSaveFailureRestoresState(t *testing.T) {
	s, _, store := setup(t)
	s.store = attendanceFailStore{store}
	if _, _, _, err := s.Handle("/Attendance", req(1)); err == nil {
		t.Fatal("failed save accepted")
	}
	if len(s.state.Attendance) != 0 || len(s.state.LoginDays) != 0 || len(s.state.Receipts) != 0 {
		t.Fatal("failed save retained new state")
	}
	persisted, err := store.Load("eventtasks")
	if err != nil || len(persisted) != 0 {
		t.Fatal("failed save persisted attendance", err)
	}
}

func TestAttendancePreviouslyGrantedRewardsAreNotMailedAgain(t *testing.T) {
	s, eco, _ := setup(t)
	m := s.attendanceMail.(*attendanceMailStub)
	a := s.attendance(s.registry.List()[0])
	a.Group, a.Count, a.LastDay = 1, 1, s.day()
	a.Obtained["1/1"], a.History["1/1"] = true, true
	_, response, _, err := s.Handle("/Attendance", req(1))
	if err != nil || claimEntries(response) != 0 || m.calls != 0 || eco.calls != 0 {
		t.Fatal("existing direct reward mailed again", err)
	}
}
func TestExplicitAttendanceClaimUsesMailboxAndSharedLedger(t *testing.T) {
	s, eco, _ := setup(t)
	m := s.attendanceMail.(*attendanceMailStub)
	if _, _, _, err := s.Handle("/LoginEvent", req(1)); err != nil {
		t.Fatal(err)
	}
	claim := wire.AppendVarint(req(2), 2, 1)
	claim = wire.AppendVarint(claim, 3, 1)
	claim = wire.AppendVarint(claim, 4, 1)
	_, response, _, err := s.Handle("/EventReward", claim)
	if err != nil || len(response) != 0 || m.calls != 1 || eco.calls != 0 || m.identity != "test:/EventReward:2" {
		t.Fatal("explicit claim bypassed mailbox", err)
	}
	_, response, _, err = s.Handle("/Attendance", req(3))
	if err != nil || claimEntries(response) != 0 || m.calls != 1 || eco.calls != 0 {
		t.Fatal("auto duplicated explicit mail", err)
	}
}
func TestAttendanceMissingMailboxDoesNotGrantOrPersist(t *testing.T) {
	s, eco, _ := setup(t)
	s.AttachAttendanceMail(nil)
	if _, _, _, err := s.Handle("/Attendance", req(1)); err == nil {
		t.Fatal("missing mailbox accepted")
	}
	if eco.calls != 0 || len(s.state.Attendance) != 0 || len(s.state.LoginDays) != 0 || len(s.state.Receipts) != 0 {
		t.Fatal("missing mailbox granted or retained state")
	}
}
