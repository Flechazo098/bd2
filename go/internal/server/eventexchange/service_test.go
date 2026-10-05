package eventexchange

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"fmt"
	"testing"
	"time"
)

type fakeRuntime struct {
	paid, grants uint64
	fail         bool
	uses         []player.Item
}

func (r *fakeRuntime) Apply(_ string, costs, rewards []gamedata.Reward) ([]byte, error) {
	if r.fail {
		return nil, fmt.Errorf("failed")
	}
	for _, c := range costs {
		r.paid += c.Count
	}
	r.grants += uint64(len(rewards))
	return []byte{10, 0}, nil
}
func (r *fakeRuntime) ConsumeAndGrant(_ string, uses []player.Item, rs []gamedata.BattleReward) ([]byte, error) {
	if r.fail {
		return nil, fmt.Errorf("failed")
	}
	r.uses = uses
	r.grants += uint64(len(rs))
	return []byte{10, 0}, nil
}
func exchangeFixture(t *testing.T) (*Service, *fakeRuntime) {
	t.Helper()
	reg := events.NewRegistry()
	if e := reg.Replace([]events.Schedule{{UID: 99, Type: 7, ID: 1, Start: 1, End: 9999999999999}}); e != nil {
		t.Fatal(e)
	}
	g := gamedata.EventExchangeGroup{ID: 1, StartPage: 1, EndPage: 2, Repeat: true, Cost: gamedata.Reward{Type: 8, ID: 77, Count: 2}, FreeCount: 1, FreeType: 2, Entries: []gamedata.EventExchangeEntry{{ID: 1, Page: 1, KeyType: 1, Ratio: 1, SetCount: 1, Reward: gamedata.Reward{Type: 4, Count: 100}}, {ID: 2, Page: 1, Ratio: 1, SetCount: 1, Reward: gamedata.Reward{Type: 8, ID: 1000, Count: 1}}, {ID: 3, Page: 2, Ratio: 1, SetCount: 1, Reward: gamedata.Reward{Type: 4, Count: 5}}}}
	r := &fakeRuntime{}
	s, e := Open(stateio.NewMemory(), &gamedata.EventExchangeCatalog{Groups: map[uint64]gamedata.EventExchangeGroup{1: g}}, reg, r)
	if e != nil {
		t.Fatal(e)
	}
	s.now = func() time.Time { return time.UnixMilli(10000) }
	s.draw = func(uint64) (uint64, error) { return 0, nil }
	return s, r
}
func exchangeRequest(seq, count uint64, paid bool) []byte {
	b := wire.AppendVarint(nil, 1, seq)
	b = wire.AppendVarint(b, 2, 99)
	b = wire.AppendVarint(b, 3, count)
	if paid {
		item := wire.AppendVarint(nil, 1, 123)
		item = wire.AppendVarint(item, 2, 77)
		item = wire.AppendVarint(item, 3, 8)
		item = wire.AppendVarint(item, 4, count*2)
		b = wire.AppendBytes(b, 4, item)
	}
	return b
}
func TestFreePaidReplayAndKeyAdvance(t *testing.T) {
	s, r := exchangeFixture(t)
	req := exchangeRequest(1, 1, false)
	_, a, _, e := s.HandleSession("/EventExchangeReward", req, "session")
	if e != nil {
		t.Fatal(e)
	}
	_, b, _, e := s.HandleSession("/EventExchangeReward", req, "session")
	if e != nil || !bytes.Equal(a, b) || r.grants != 1 {
		t.Fatalf("replay %v %d", e, r.grants)
	}
	if _, _, _, e = s.HandleSession("/EventExchangeReward", exchangeRequest(2, 1, false), "session"); e == nil {
		t.Fatal("free reused")
	}
	next := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 3), 2, 99), 3, 1)
	if _, _, _, e = s.HandleSession("/EventExchangeNextPageOpen", next, "session"); e != nil {
		t.Fatal(e)
	}
	if _, _, _, e = s.HandleSession("/EventExchangeReward", exchangeRequest(4, 1, true), "session"); e != nil {
		t.Fatal(e)
	}
	st, _ := s.load()
	if st.Progress["99"].Page != 3 || len(r.uses) != 1 || r.uses[0].InvenIndex != 123 {
		t.Fatalf("advance %+v uses %+v", st, r.uses)
	}
}
func TestInvalidCostAndFailedGrantKeepPool(t *testing.T) {
	s, r := exchangeFixture(t)
	bad := exchangeRequest(1, 1, true)
	bad = wire.AppendBytes(bad, 4, wire.AppendVarint(nil, 3, 8))
	if _, _, _, e := s.HandleSession("/EventExchangeReward", bad, "session"); e == nil {
		t.Fatal("invalid cost accepted")
	}
	r.fail = true
	if _, _, _, e := s.HandleSession("/EventExchangeReward", exchangeRequest(2, 1, false), "session"); e == nil {
		t.Fatal("failed grant accepted")
	}
	st, _ := s.load()
	if len(st.Progress) != 0 || len(st.Receipts) != 0 {
		t.Fatal("failed draw saved progress")
	}
}
func TestLuckyPrizeLockedUntilUnlockDraw(t *testing.T) {
	s, _ := exchangeFixture(t)
	g := s.design.Groups[1]
	g.UnlockRatio = 3
	g.Entries[0].LimitedRatio = 0
	g.Entries[1].LimitedRatio = 1
	g.Entries[1].SetCount = 4
	s.design.Groups[1] = g
	for i := uint64(1); i <= 3; i++ {
		if _, _, _, e := s.HandleSession("/EventExchangeReward", exchangeRequest(i, 1, true), "session"); e != nil {
			t.Fatal(e)
		}
		st, _ := s.load()
		count := st.Progress["99"].Counts["1"]
		if i < 3 && count != 0 {
			t.Fatal("prize unlocked early")
		}
		if i == 3 && count != 1 {
			t.Fatal("prize not unlocked")
		}
	}
}
