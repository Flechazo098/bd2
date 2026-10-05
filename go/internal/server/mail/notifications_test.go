package mail

import (
	"testing"
	"time"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func TestNewMailNotificationDoesNotRepeatOnUnchangedRequests(t *testing.T) {
	s, _, _ := attendanceMailFixture(t, stateio.NewMemory(), &Starter{Version: "2.35.10", MailCount: 1}, &attendanceDesign{})
	now := time.Now().UTC()
	if err := s.BeforeDispatch("/Attendance", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.IssueAttachmentsOnce("today", "Attendance Rewards", "Claim your rewards.", []gamedata.Reward{{Type: 9, ID: 100, Count: 1}}, now); err != nil {
		t.Fatal(err)
	}
	notice, err := s.AfterDispatch("/Attendance", nil, nil)
	if v, ok, parseErr := wire.Varint(notice, 1); err != nil || parseErr != nil || !ok || v != 1 {
		t.Fatal("new mail did not notify", err, parseErr)
	}
	if err := s.BeforeDispatch("/read", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.IssueAttachmentsOnce("today", "Attendance Rewards", "Claim your rewards.", []gamedata.Reward{{Type: 9, ID: 100, Count: 1}}, now); err != nil {
		t.Fatal(err)
	}
	if notice, err := s.AfterDispatch("/read", nil, nil); err != nil || len(notice) != 0 {
		t.Fatal("unchanged mail notified again", err)
	}
}
