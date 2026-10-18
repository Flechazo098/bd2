//go:build integration

package roguelike

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"math"
	"slices"
	"testing"
)

// LocalTextTable 1600225 defines the first five distinct choices, later two
// acquired/two unacquired offers, and account ownership for new/collaboration costumes.
func TestCostumeOffersRespectAccountOwnershipAndRunAcquisitions(t *testing.T) {
	d := currentDesign(t)
	var ordinary, restricted []uint64
	for _, id := range d.CostumeIDs {
		if d.Costumes[id].MaxLevel == 0 {
			continue
		}
		if d.Costumes[id].RequiresOwnership {
			restricted = append(restricted, id)
		} else {
			ordinary = append(ordinary, id)
		}
	}
	if len(ordinary) < 12 || len(restricted) < 2 {
		t.Fatal("real design lacks distinct eligible ownership pools")
	}
	account := map[uint64]bool{restricted[0]: true}
	s := &Service{design: d, options: Options{CostumeOwned: func(id uint64) bool { return account[id] }}, sample: func(uint64) uint64 { return 0 }}
	r := &run{Level: 1}
	if e := s.newChoice(r, 1, 0, true); e != nil {
		t.Fatal(e)
	}
	if !slices.Contains(r.Choice, restricted[0]) || slices.Contains(r.Choice, restricted[1]) || !slices.Contains(r.Choice, ordinary[0]) {
		t.Fatal("wildcard did not distinguish account-owned restricted costumes from ordinary unowned costumes")
	}
	delete(account, restricted[0])
	if e := s.newChoice(r, 1, 0, true); e != nil {
		t.Fatal(e)
	}
	if slices.Contains(r.Choice, restricted[0]) {
		t.Fatal("account ownership changes were ignored")
	}
	account[restricted[0]] = true
	acquired := []uint64{ordinary[0], ordinary[1], ordinary[2], restricted[0]}
	for i, id := range acquired {
		r.Costumes = append(r.Costumes, costume{Index: uint64(i + 1), ID: id})
	}
	maxed := ordinary[3]
	r.Costumes = append(r.Costumes, costume{Index: 5, ID: maxed, Level: d.Costumes[maxed].MaxLevel})
	if e := s.newChoice(r, 1, 0, false); e != nil {
		t.Fatal(e)
	}
	for _, id := range r.Choice {
		if slices.Contains(acquired, id) || id == maxed {
			t.Fatal("initial draft offered an already acquired costume")
		}
	}
	r.Room, r.Floor = 1, 1
	check := func(ids []uint64, expectedOwned int) {
		t.Helper()
		seen := map[uint64]bool{}
		owned := 0
		for _, id := range ids {
			if seen[id] || id == maxed || (d.Costumes[id].RequiresOwnership && !account[id]) {
				t.Fatalf("invalid, duplicate, maxed, or account-locked offer %d", id)
			}
			seen[id] = true
			if slices.Contains(acquired, id) {
				owned++
			}
		}
		if owned != expectedOwned {
			t.Fatalf("acquired offers = %d, want %d (total %d)", owned, expectedOwned, len(ids))
		}
	}
	if e := s.newChoice(r, 1, 0, false); e != nil {
		t.Fatal(e)
	}
	if len(r.Choice) != 4 {
		t.Fatal("normal reward lacks four choices despite sufficient eligible pools")
	}
	check(r.Choice, 2)
	for key, rule := range d.Rooms.Rows {
		if rule.V(10) == 6 && d.Shops.Row(0, rule.V(9)).Values != nil {
			r.Floors = []floor{{Number: 1, Rooms: []room{{Number: 1, Group: key[0], ID: key[1]}}}}
			break
		}
	}
	if len(r.Floors) == 0 {
		t.Fatal("real design has no shop room")
	}
	v := &state{Run: r}
	if e := s.newShop(v); e != nil {
		t.Fatal(e)
	}
	var shopIDs []uint64
	for _, o := range r.Shop {
		if o.Type == 1 {
			shopIDs = append(shopIDs, o.ID)
		}
	}
	if len(shopIDs) != 8 {
		t.Fatal("real shop lacks eight costume offers despite sufficient eligible pools")
	}
	check(shopIDs, 4)
	_, upgraded, e := s.addCostume(command.Context{}, v, acquired[0])
	if e != nil || upgraded.Level != 1 || len(r.Costumes) != 5 {
		t.Fatal("a later acquired offer cannot upgrade its existing run costume")
	}
	r.Costumes = nil
	for i, id := range d.CostumeIDs {
		level := d.Costumes[id].MaxLevel
		if slices.Contains(acquired[:3], id) {
			level = 0
		}
		r.Costumes = append(r.Costumes, costume{Index: uint64(i + 1), ID: id, Level: level})
	}
	if e = s.newChoice(r, 1, 0, false); e != nil {
		t.Fatalf("exhausted unacquired pool blocked the run: %v", e)
	}
	if len(r.Choice) != 3 {
		t.Fatal("pool exhaustion should return each remaining upgrade once")
	}
	check(r.Choice, 3)
}

// ScheduleSeason transitions to off-season after End+Calculate while retaining
// the same season ID; Info triggers the native reward request through field 21.
func TestSeasonRewardUnlocksAfterCalculationWithoutSeasonIDChange(t *testing.T) {
	d := currentDesign(t)
	store := stateio.NewMemory()
	ctx := command.Context{Identity: command.Identity{AccountID: "settled-player", SessionID: "settled-login"}, State: store}
	period := Period{Season: 97}
	settled := false
	economy := &testEconomy{}
	open := func() *Service {
		t.Helper()
		s, e := Open(stateio.EntrySnapshotStore{Domain: "battle", Bucket: "gameplay"}, d, Options{
			Access: func(command.Context) error { return nil }, Economy: economy,
			Period:        func(command.Context) (Period, error) { return period, nil },
			Tickets:       func(command.Context, string, uint64) error { return nil },
			SeasonSettled: func(id uint64) bool { return id == period.Season && settled },
			Ranking:       seasonBoard{total: math.MaxInt64},
		})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	s := open()
	v, _, e := s.load(ctx)
	if e != nil {
		t.Fatal(e)
	}
	v.Seasons[period.Season] = seasonRecord{Score: 1}
	if e = s.save(ctx, v); e != nil {
		t.Fatal(e)
	}
	if info := rpc(t, s, ctx, "Info", req(1)); n(info, 19) != period.Season || n(info, 21) != 0 {
		t.Fatal("unsettled season advertised a reward")
	}
	assertRejected(t, s, ctx, "SeasonReward", req(2))
	if economy.calls != 0 {
		t.Fatal("calculation paid before settlement")
	}
	settled = true
	s = open()
	if info := rpc(t, s, ctx, "Info", req(3)); n(info, 19) != period.Season || n(info, 21) != 1 {
		t.Fatal("settled current season did not advertise its native reward request")
	}
	claim := req(4)
	rpc(t, s, ctx, "SeasonReward", claim)
	if economy.calls != 1 || economy.rewards == 0 {
		t.Fatal("settled same-ID season did not pay actual threshold rewards")
	}
	s = open()
	rpc(t, s, ctx, "SeasonReward", claim)
	assertRejected(t, s, ctx, "SeasonReward", req(5))
	if info := rpc(t, s, ctx, "Info", req(6)); n(info, 21) != 0 || economy.calls != 1 {
		t.Fatal("settled claim did not persist or retry paid again")
	}
}
