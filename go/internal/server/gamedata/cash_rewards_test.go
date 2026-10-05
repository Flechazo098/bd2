package gamedata

import (
	"bd2server/internal/server/wire"
	"testing"
)

func TestCashCombinationsGuaranteeOneAndHundredPreserveManualBoxes(t *testing.T) {
	raw := wire.AppendVarint(nil, 2, 1)
	for _, f := range []struct {
		n int
		v uint64
	}{{6, 3}, {6, 8}, {5, 0}, {5, 1000}, {4, 60}, {4, 2}, {8, 1}, {8, 100}} {
		raw = wire.AppendVarint(raw, f.n, f.v)
	}
	c := &CashRewardResolver{boxes: map[uint64]uint64{1: 10}, direct: map[uint64]bool{1: true}, groups: map[uint64][]byte{10: raw}}
	got, err := c.ResolveGranted([]BattleReward{{Type: 9, ID: 1, Count: 2}, {Type: 8, ID: 2, Count: 1}})
	if err != nil || len(got) != 3 || got[0].Type != 3 || got[0].Count != 120 || got[1].Count != 4 || got[2].ID != 2 || got[2].Type != 8 {
		t.Fatalf("cash rewards %+v %v", got, err)
	}
	raw = wire.AppendVarint(raw, 8, 35)
	c.groups[10] = raw
	if _, err = c.ResolveGranted([]BattleReward{{Type: 9, ID: 1, Count: 1}}); err == nil {
		t.Fatal("malformed ratio accepted")
	}
}

func TestLoginPassTopLevelWrapperExpandsAndNestedManualGiftStaysOwned(t *testing.T) {
	raw := wire.AppendVarint(nil, 2, 1)
	for _, f := range []struct {
		n int
		v uint64
	}{{6, 3}, {6, 9}, {5, 0}, {5, 200}, {4, 125}, {4, 1}, {8, 1}, {8, 100}} {
		raw = wire.AppendVarint(raw, f.n, f.v)
	}
	c := &CashRewardResolver{boxes: map[uint64]uint64{100: 1000, 200: 2000}, direct: map[uint64]bool{}, groups: map[uint64][]byte{1000: raw}}
	got, err := c.ResolveGranted([]BattleReward{{Type: 9, ID: 100, Count: 1}})
	if err != nil || len(got) != 2 || got[0].Type != 3 || got[0].Count != 125 || got[1].Type != 9 || got[1].ID != 200 {
		t.Fatalf("login pass wrapper=%+v err=%v", got, err)
	}
}
