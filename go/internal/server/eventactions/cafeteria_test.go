package eventactions

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"bytes"
	"testing"
	"time"
)

func TestCafeteriaActiveScheduleDailyCapCooldownAndReplay(t *testing.T) {
	s, e, store := setup(t)
	now := s.now()
	s.now = func() time.Time { return now }
	s.design.Tables["CafeteriaDefaultTable"] = []gamedata.EventActionRow{row(map[int]uint64{8: 5, 3: 2, 28: 2})}
	s.design.Tables["CafeteriaEventTable"] = []gamedata.EventActionRow{row(map[int]uint64{5: 1, 6: 1, 4: 1, 11: 3, 13: 44})}
	b := wire.AppendVarint(req(1), 2, 1)
	b = wire.AppendVarint(b, 3, 1)
	if _, _, _, err := s.Handle("/CafeteriaEventNpcInteractionReward", b); err == nil {
		t.Fatal("inactive cafeteria accepted")
	}
	r := events.NewRegistry()
	if err := r.Replace([]events.Schedule{{UID: 77, Type: 25, ID: 3007, SubID: 1, Start: now.Add(-time.Hour).UnixMilli(), End: now.Add(48 * time.Hour).UnixMilli()}}); err != nil {
		t.Fatal(err)
	}
	s.registry = r
	code, reply, _, err := s.Handle("/CafeteriaEventNpcInteractionReward", b)
	if err != nil || code != 414 || e.calls != 1 {
		t.Fatalf("cafeteria%d err%v", code, err)
	}
	n, err := Open(store, s.design, r, e)
	if err != nil {
		t.Fatal(err)
	}
	n.now = s.now
	n.BeginSession("test")
	_, again, _, err := n.Handle("/CafeteriaEventNpcInteractionReward", b)
	if err != nil || !bytes.Equal(reply, again) || e.calls != 1 {
		t.Fatal("replay grants twice")
	}
	b2 := wire.AppendVarint(req(2), 2, 1)
	b2 = wire.AppendVarint(b2, 3, 1)
	if _, _, _, err = n.Handle("/CafeteriaEventNpcInteractionReward", b2); err == nil {
		t.Fatal("cooldown bypass")
	}
	now = now.Add(2 * time.Second)
	_, reply, _, err = n.Handle("/CafeteriaEventNpcInteractionReward", b2)
	if err != nil {
		t.Fatal(err)
	}
	count, _, _ := wire.Varint(reply, 2)
	if count != 5 || e.rewards[len(e.rewards)-1].Count != 2 {
		t.Fatal("daily cap not clamped")
	}
	b3 := wire.AppendVarint(req(3), 2, 1)
	b3 = wire.AppendVarint(b3, 3, 1)
	now = now.Add(24 * time.Hour)
	if _, _, _, err = n.Handle("/CafeteriaEventNpcInteractionReward", b3); err != nil || n.state.CafeteriaCurrency != 3 {
		t.Fatal("daily limit failed to reset")
	}
}
