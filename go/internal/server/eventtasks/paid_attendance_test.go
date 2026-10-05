package eventtasks

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"bytes"
	"testing"
)

func TestAttendancePremiumRequiresOwnedTicketAndDoesNotDuplicate(t *testing.T) {
	for _, paid := range []bool{false, true} {
		s, eco, store := setup(t)
		m := s.attendanceMail.(*attendanceMailStub)
		s.design.Attendance[1] = gamedata.EventAttendance{ID: 1, Group: 1, Ticket: 77}
		s.design.AttendanceRewards[1] = []gamedata.EventAttendanceReward{{ID: 1, Group: 1, Day: 1, Basic: gamedata.Reward{Type: 4, Count: 100}, Premium: gamedata.Reward{Type: 4, Count: 100}}}
		s.AttachAttendancePremium(func(ticket uint64) bool { return paid && ticket == 77 })
		claim := req(1)
		code, response, handled, err := s.Handle("/Attendance", claim)
		if err != nil || code != 0 || !handled {
			t.Fatal(err)
		}

		if _, present, _ := wire.Bytes(response, 1001); present {
			t.Fatal("attendance directly granted rewards")
		}
		if m.identity != "test:/Attendance:1" || m.title == "" || m.body == "" || !m.sentAt.Equal(s.now()) {
			t.Fatal("mail identity or content absent")
		}

		want := 1
		if paid {
			want = 2
		}
		if len(m.rewards) != want {
			t.Fatal("premium ticket ignored", paid, m.rewards)
		}
		if _, replay, _, err := s.Handle("/Attendance", claim); err != nil || m.calls != 1 || eco.calls != 0 || !bytes.Equal(replay, response) {
			t.Fatal("claim replay duplicated", err)
		}
		reopened, err := Open(store, s.design, s.registry, eco)
		if err != nil {
			t.Fatal(err)
		}
		reopened.AttachAttendanceMail(m)
		reopened.now = s.now
		reopened.SetSession("test")
		_, replay, _, err := reopened.Handle("/Attendance", claim)
		if err != nil || m.calls != 1 || eco.calls != 0 || !bytes.Equal(replay, response) {
			t.Fatal("restart lost reward envelope", err)
		}
	}
}
