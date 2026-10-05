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
	c := &CashRewardResolver{boxes: map[uint64]uint64{100: 1000, 200: 2000}, direct: map[uint64]bool{100: false, 200: true}, groups: map[uint64][]byte{1000: raw}}
	got, err := c.ResolveGranted([]BattleReward{{Type: 9, ID: 100, Count: 1}})
	if err != nil || len(got) != 2 || got[0].Type != 3 || got[0].Count != 125 || got[1].Type != 9 || got[1].ID != 200 {
		t.Fatalf("login pass wrapper=%+v err=%v", got, err)
	}
}

func TestCashDeliverySeparatesNestedMailAndKeepsEntitlementAndManualBox(t *testing.T) {
	group := func(mail uint64, rewards []BattleReward) []byte {
		raw := wire.AppendVarint(nil, 2, 1)
		if mail > 0 {
			raw = wire.AppendVarint(raw, 7, mail)
		}
		for _, r := range rewards {
			raw = wire.AppendVarint(raw, 6, r.Type)
			raw = wire.AppendVarint(raw, 5, r.ID)
			raw = wire.AppendVarint(raw, 4, r.Count)
			raw = wire.AppendVarint(raw, 8, 100)
		}
		return raw
	}
	c := &CashRewardResolver{boxes: map[uint64]uint64{1: 10, 2: 20, 3: 30}, direct: map[uint64]bool{3: true}, groups: map[uint64][]byte{
		10: group(0, []BattleReward{{Type: 19, ID: 36, Count: 1}, {Type: 9, ID: 2, Count: 1}}),
		20: group(40, []BattleReward{{Type: 2, Count: 150}, {Type: 9, ID: 3, Count: 1}}),
	}}
	plan, err := c.ResolveDelivery([]BattleReward{{Type: 9, ID: 1, Count: 2}})
	if err != nil || len(plan.Direct) != 1 || plan.Direct[0].Type != 19 || plan.Direct[0].Count != 2 || len(plan.Mail) != 1 || plan.Mail[0].TemplateID != 40 || len(plan.Mail[0].Rewards) != 2 || plan.Mail[0].Rewards[0].Count != 300 || plan.Mail[0].Rewards[1].ID != 3 {
		t.Fatalf("delivery=%+v err=%v", plan, err)
	}
	// Claim expansion retains the old behavior for callers that don't purchase.
	leaves, err := c.ResolveGranted([]BattleReward{{Type: 9, ID: 1, Count: 1}})
	if err != nil || len(leaves) != 3 {
		t.Fatalf("ordinary resolve=%+v err=%v", leaves, err)
	}
}
