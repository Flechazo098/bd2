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

type entitlementGraph struct{ Calls int }

func (g *entitlementGraph) ResolveGranted(r []gamedata.BattleReward) ([]gamedata.BattleReward, error) {
	g.Calls++
	return r, nil
}

type entitlementBase struct {
	Calls   int
	Rewards []gamedata.Reward
}

func (b *entitlementBase) Apply(_ string, _ []gamedata.Reward, r []gamedata.Reward) ([]byte, error) {
	b.Calls++
	b.Rewards = append(b.Rewards, r...)
	return wire.AppendVarint(nil, 7, 1), nil
}
func entitlementFixture(t *testing.T) (*EntitlementEconomy, *player.Inventory, *entitlementBase, *entitlementGraph, *time.Time) {
	t.Helper()
	store := stateio.NewMemory()
	items, err := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	d := &gamedata.CashEntitlementDesign{AvatarSets: map[uint64]bool{10: true}, TicketTypes: map[uint64]uint64{38: 2, 99: 3}, Attendance: map[uint64][]gamedata.CashAttendanceReward{38: {{ID: 1, Reward: gamedata.BattleReward{Type: 3, Count: 1}}, {ID: 2, Reward: gamedata.BattleReward{Type: 3, Count: 5}}}, 99: {{ID: 1, Reward: gamedata.BattleReward{Type: 3, Count: 1}}, {ID: 2, Reward: gamedata.BattleReward{Type: 3, Count: 7}}}}, AttendanceTypes: map[uint64]uint64{38: 1}}
	base := &entitlementBase{}
	graph := &entitlementGraph{}
	e, err := NewEntitlementEconomy(store, base, graph, items, d)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	e.SetClock(func() time.Time { return now }, 0)
	return e, items, base, graph, &now
}
func TestEntitlementTypedSkinExpiryRetryAndRenewal(t *testing.T) {
	e, items, base, graph, now := entitlementFixture(t)
	rewards := []gamedata.Reward{{Type: 62, ID: 10, Count: 1}, {Type: 19, ID: 38, Count: 1}, {Type: 45, ID: 999, Count: 1}}
	first, err := e.Apply("buy1", nil, rewards)
	if err != nil {
		t.Fatal(err)
	}
	if items.ContentTicketExpiry(38) != uint64(now.UnixMilli()+30*86400000) || len(base.Rewards) != 2 || base.Rewards[0].Type != 45 {
		t.Fatal("expiry or prestige dispatch invalid")
	}
	again, err := e.Apply("buy1", nil, rewards)
	if err != nil || !bytes.Equal(first, again) || graph.Calls != 2 || base.Calls != 1 {
		t.Fatal("retry rerolled or double granted")
	}
	if _, err = e.Apply("buy1", nil, nil); err == nil {
		t.Fatal("identity reuse accepted")
	}
	if _, err = e.Apply("invalid", nil, []gamedata.Reward{{Type: 62, ID: 888, Count: 1}}); err == nil || base.Calls != 1 {
		t.Fatal("unknown skin partially applied")
	}
	if _, err = e.Apply("buy2", nil, []gamedata.Reward{{Type: 19, ID: 38, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	if items.ContentTicketExpiry(38) != uint64(now.UnixMilli()+60*86400000) {
		t.Fatal("renewal lost prior period")
	}
	merged, err := e.MergeAttendance(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := wire.Bytes(merged, 3); !ok {
		t.Fatal("subscription info missing")
	}
	if _, ok, _ := wire.Bytes(merged, 7); !ok {
		t.Fatal("monthly reward map missing")
	}
}
func TestSubscriptionDailyClaimResetExpiryAndPermanentCompletion(t *testing.T) {
	e, _, base, _, now := entitlementFixture(t)
	_, err := e.Apply("buy", nil, []gamedata.Reward{{Type: 19, ID: 38, Count: 1}, {Type: 19, ID: 99, Count: 1}})
	if err != nil {
		t.Fatal(err)
	}
	calls := base.Calls
	if _, err = e.ClaimSubscriptions("login"); err != nil || base.Calls != calls {
		t.Fatal("purchase day double claim")
	}
	*now = now.Add(24 * time.Hour)
	if _, err = e.ClaimSubscriptions("login2"); err != nil || base.Calls != calls+2 {
		t.Fatalf("daily claims %v calls%d", err, base.Calls)
	}
	calls = base.Calls
	if _, err = e.ClaimSubscriptions("retry"); err != nil || base.Calls != calls {
		t.Fatal("daily retry grants again")
	}
	*now = now.Add(31 * 24 * time.Hour)
	if _, err = e.ClaimSubscriptions("expired"); err != nil || base.Calls != calls {
		t.Fatal("expired or completed claim")
	}
	merged, err := e.MergeAttendance(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := wire.Bytes(merged, 4); !ok {
		t.Fatal("permanent attendance markers missing")
	}
}
func TestSubscriptionFirstRewardAndLostReply(t *testing.T) {
	e, _, base, _, now := entitlementFixture(t)
	_, err := e.Apply("covered", nil, []gamedata.Reward{{Type: 19, ID: 38, Count: 1}, {Type: 3, Count: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(base.Rewards) != 1 || base.Rewards[0].Count != 1 {
		t.Fatalf("first-day reward duplicated: %+v", base.Rewards)
	}
	*now = now.Add(24 * time.Hour)
	first, err := e.ClaimSubscriptions("same-session-request")
	if err != nil || len(first) == 0 {
		t.Fatalf("claim %x %v", first, err)
	}
	calls := base.Calls
	replay, err := e.ClaimSubscriptions("same-session-request")
	if err != nil || !bytes.Equal(first, replay) || base.Calls != calls {
		t.Fatal("lost reply not replayed exactly")
	}
	fresh, err := e.ClaimSubscriptions("new-request-same-day")
	if err != nil || len(fresh) != 0 || base.Calls != calls {
		t.Fatal("new same-day request repeated award")
	}
}
