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
		s.design.Attendance[1] = gamedata.EventAttendance{ID: 1, Group: 1, Ticket: 77}
		s.design.AttendanceRewards[1] = []gamedata.EventAttendanceReward{{ID: 1, Group: 1, Day: 1, Basic: gamedata.Reward{Type: 4, Count: 100}, Premium: gamedata.Reward{Type: 4, Count: 100}}}
		s.AttachAttendancePremium(func(ticket uint64) bool { return paid && ticket == 77 })
		claim := req(1)
		code, response, handled, err := s.Handle("/Attendance", claim)
		if err != nil || code != 0 || !handled {
			t.Fatal(err)
		}

		bundle, ok, err := wire.Bytes(response, 1001)
		if err != nil || !ok || !bytes.Equal(bundle, []byte{10, 0}) {
			t.Fatal("actual bundle absent", bundle, err)
		}
		receipt, ok, err := wire.Bytes(response, 1002)
		if err != nil || !ok || string(receipt) != "test:/Attendance:1" {
			t.Fatal("durable receipt absent", string(receipt), err)
		}
		want := 1
		if paid {
			want = 2
		}
		if len(eco.rewards) != want {
			t.Fatal("premium ticket ignored", paid, eco.rewards)
		}
		if _, replay, _, err := s.Handle("/Attendance", claim); err != nil || eco.calls != 1 || !bytes.Equal(replay, response) {
			t.Fatal("claim replay duplicated", err)
		}
		reopened, err := Open(store, s.design, s.registry, eco)
		if err != nil {
			t.Fatal(err)
		}
		reopened.now = s.now
		reopened.SetSession("test")
		_, replay, _, err := reopened.Handle("/Attendance", claim)
		if err != nil || eco.calls != 1 || !bytes.Equal(replay, response) {
			t.Fatal("restart lost reward envelope", err)
		}
	}
}
