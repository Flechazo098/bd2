package commerce

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"testing"
	"time"
)

type loginPassEconomy struct {
	calls   int
	rewards [][]gamedata.Reward
}

func (e *loginPassEconomy) Apply(_ string, _ []gamedata.Reward, r []gamedata.Reward) ([]byte, error) {
	e.calls++
	e.rewards = append(e.rewards, append([]gamedata.Reward(nil), r...))
	return []byte{8, 1}, nil
}
func TestLoginPassFreeDaysPaidCatchupAndRestart(t *testing.T) {
	d := &gamedata.LoginPassCatalog{Groups: map[uint64][]gamedata.LoginPassReward{10: {{ID: 1, TicketID: 77, Free: gamedata.Reward{Type: 9, ID: 100, Count: 1}, Premium: gamedata.Reward{Type: 9, ID: 200, Count: 1}}, {ID: 2, TicketID: 77, Free: gamedata.Reward{Type: 9, ID: 101, Count: 1}, Premium: gamedata.Reward{Type: 9, ID: 201, Count: 1}}}, 20: {{ID: 1, TicketID: 88, Free: gamedata.Reward{Type: 9, ID: 300, Count: 1}, Premium: gamedata.Reward{Type: 9, ID: 400, Count: 1}}}}}
	store := stateio.NewMemory()
	eco := &loginPassEconomy{}
	items := &clearInventory{}
	available := func(group uint64) bool { return group == 10 }
	s, err := NewLoginPasses(store, d, eco, items, available)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now }, 0)
	bundle, infos, err := s.ClaimAndInfo("attendance1")
	if err != nil || eco.calls != 1 || len(eco.rewards[0]) != 1 || eco.rewards[0][0].ID != 100 || len(infos) != 1 {
		t.Fatal(err, eco.rewards, infos)
	}
	replay, _, err := s.ClaimAndInfo("attendance1")
	if err != nil || !bytes.Equal(bundle, replay) || eco.calls != 1 {
		t.Fatal("duplicate login grant", err)
	}
	_, infos, err = s.ClaimAndInfo("attendance2")
	rewarded, _, _ := wire.Varint(infos[0], 3)
	if err != nil || eco.calls != 1 || rewarded != 0 {
		t.Fatal("same-day advanced")
	}
	items.items = []player.Item{{Type: 19, ID: 77, Count: 1}}
	_, _, err = s.ClaimAndInfo("after-buy")
	if err != nil || eco.calls != 2 || len(eco.rewards[1]) != 1 || eco.rewards[1][0].ID != 200 {
		t.Fatal("premium catchup missing", err, eco.rewards)
	}
	now = now.Add(48 * time.Hour)
	_, infos, err = s.ClaimAndInfo("next-login")
	day, _, _ := wire.Varint(infos[0], 2)
	if err != nil || day != 2 || eco.calls != 3 || len(eco.rewards[2]) != 2 {
		t.Fatal("missed day incorrectly advanced", err, day)
	}
	s, err = NewLoginPasses(store, d, eco, items, available)
	if err != nil {
		t.Fatal(err)
	}
	s.SetClock(func() time.Time { return now }, 0)
	_, _, err = s.ClaimAndInfo("restart")
	if err != nil || eco.calls != 3 {
		t.Fatal("restart advanced complete pass", err)
	}
}
