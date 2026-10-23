//go:build integration

package cafeteria

import (
	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"bytes"
	"fmt"
	"maps"
	"testing"
	"time"
)

type ledger struct {
	balances map[[2]uint64]uint64
}

func (l *ledger) Apply(_ command.Context, _ string, costs, rewards []gamedata.Reward) ([]byte, error) {
	for _, r := range costs {
		if l.balances[[2]uint64{r.Type, r.ID}] < r.Count {
			return nil, fmt.Errorf("insufficient funds")
		}
	}
	for _, r := range costs {
		l.balances[[2]uint64{r.Type, r.ID}] -= r.Count
	}
	for _, r := range rewards {
		l.balances[[2]uint64{r.Type, r.ID}] += r.Count
	}
	return nil, nil
}
func liveDesign(t *testing.T) *gamedata.CafeteriaDesign {
	t.Helper()
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	versionconfig.Use(versions)
	root, version := versions.Resolve("data/resources/GameData"), versions.GameDataVersion
	d, e := gamedata.LoadCafeteriaDesign(root, version)
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func TestCafeteriaBusinessSurvivesLoginAndDailyReset(t *testing.T) {
	d := liveDesign(t)
	store := stateio.NewMemory()
	ctx := command.Context{Identity: command.Identity{SessionID: "business"}, State: store}
	money := &ledger{balances: map[[2]uint64]uint64{}}
	now := time.Date(2026, 10, 17, 0, 1, 0, 0, time.UTC)
	costume := d.Costumes.Groups[0][0]
	open := func() *Service {
		s, e := Open(ctx, stateio.RootStore{}, d, money)
		if e != nil {
			t.Fatal(e)
		}
		s.now = func() time.Time { return now }
		s.AttachOwnedCostumes(func() []uint64 { return []uint64{costume} })
		s.AttachDatingHasEpisode(func(uint64) bool { return true })
		return s
	}
	s := open()
	seq := uint64(0)
	call := func(path string, id uint64) ([]byte, error) {
		seq++
		b := wire.AppendVarint(nil, 1, seq)
		if id > 0 {
			b = wire.AppendVarint(b, 2, id)
		}
		_, out, handled, e := s.Handle(ctx, path, b)
		if !handled {
			t.Fatalf("unhandled %s", path)
		}
		return out, e
	}
	must := func(path string, id uint64) []byte {
		t.Helper()
		out, e := call(path, id)
		if e != nil {
			t.Fatalf("%s: %v", path, e)
		}
		return out
	}
	info := must("/CafeteriaInfo", 0)
	base, _, e := wire.Bytes(info, 1)
	if e != nil || val(base, 1) == 0 || val(base, 2) == 0 {
		t.Fatal("client requires initialized business and receipt time")
	}
	if _, e = call("/CafeteriaCumulativeReward", 0); e == nil {
		t.Fatal("settlement before minimum time succeeded")
	}
	if _, e = call("/CafeteriaLevelUp", 0); e == nil {
		t.Fatal("level advanced before management finished")
	}
	level := val(base, 1)
	manage, _ := d.Manage.Row(level, 1)
	if manage.V(3) > 0 {
		if _, e = call("/CafeteriaManageItemAdd", 0); e == nil {
			t.Fatal("unfunded upgrade succeeded")
		}
		money.balances[[2]uint64{manage.V(2), manage.V(1)}] = manage.V(3)
	}
	must("/CafeteriaManageItemAdd", 0)
	productionBefore := maps.Clone(money.balances)
	now = now.Add(time.Duration(d.Default.V(17))*time.Minute + time.Second)
	must("/CafeteriaCumulativeReward", 0)
	if maps.Equal(productionBefore, money.balances) {
		t.Fatal("business generated no production rewards")
	}
	for _, manageID := range d.Manage.Groups[level] {
		if manageID <= 1 {
			continue
		}
		step, _ := d.Manage.Row(level, manageID)
		currency := [2]uint64{step.V(2), step.V(1)}
		money.balances[currency] += step.V(3)
		beforeCost := money.balances[currency]
		must("/CafeteriaManageItemAdd", 0)
		if money.balances[currency]+step.V(3) != beforeCost {
			t.Fatal("management upgrade did not debit its cost")
		}
	}
	upgraded := must("/CafeteriaLevelUp", 0)
	newBusiness, _, _ := wire.Bytes(upgraded, 1)
	if val(newBusiness, 1) != level+1 || val(newBusiness, 4) != 0 {
		t.Fatal("completed management did not advance restaurant level")
	}
	before := must("/CafeteriaInfo", 0)
	s = open()
	after := must("/CafeteriaInfo", 0)
	if !bytes.Equal(before, after) {
		t.Fatal("business state changed across login")
	}
	for day := uint64(0); day < d.Default.V(7); day++ {
		if day > 0 {
			now = now.Add(24 * time.Hour)
			must("/CafeteriaSpawnReset", 0)
		}
		must("/CafeteriaRegularCostumeInteractionReward", costume)
		balances := maps.Clone(money.balances)
		must("/CafeteriaRegularCostumeInteractionReward", costume)
		if !maps.Equal(money.balances, balances) {
			t.Fatal("daily interaction was rewarded twice")
		}
		notes := must("/CafeteriaRegularCostumeNoteInfo", 0)
		n, _, _ := wire.Bytes(notes, 1)
		if val(n, 2) != day+1 {
			t.Fatal("duplicate daily serve advanced visit progress")
		}
		s = open()
	}
	balanceKey := [2]uint64{d.Default.V(20), d.Default.V(19)}
	noteBefore := money.balances[balanceKey]
	must("/CafeteriaRegularCostumeNoteReward", costume)
	if money.balances[balanceKey]-noteBefore != d.Default.V(21) {
		t.Fatal("first note claim did not grant the configured reward")
	}
	earned := money.balances[balanceKey]
	must("/CafeteriaRegularCostumeNoteReward", costume)
	if money.balances[balanceKey] != earned {
		t.Fatal("note reward was granted twice")
	}
	s = open()
	n, _, _ := wire.Bytes(must("/CafeteriaRegularCostumeNoteInfo", 0), 1)
	if val(n, 3) != 1 {
		t.Fatal("note claim did not survive login")
	}
}
func TestCafeteriaInteractionCapIsSharedAndResets(t *testing.T) {
	d := liveDesign(t)
	store := stateio.NewMemory()
	ctx := command.Context{Identity: command.Identity{SessionID: "cap"}, State: store}
	money := &ledger{balances: map[[2]uint64]uint64{}}
	s, e := Open(ctx, stateio.RootStore{}, d, money)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 10, 17, 0, 1, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.AttachOwnedCostumes(func() []uint64 { return nil })
	s.AttachDatingHasEpisode(func(uint64) bool { return true })
	var event [2]uint64
	for k, r := range d.Events.Rows {
		if r.V(13) == d.Default.V(12) && r.V(12) == d.Default.V(11) && r.V(11) > 0 {
			event = k
			break
		}
	}
	seq := uint64(0)
	call := func() []byte {
		seq++
		b := wire.AppendVarint(nil, 1, seq)
		b = wire.AppendVarint(b, 2, event[0])
		b = wire.AppendVarint(b, 3, event[1])
		_, out, _, e := s.Handle(ctx, "/CafeteriaEventNpcInteractionReward", b)
		if e != nil {
			t.Fatal(e)
		}
		return out
	}
	cap := d.Default.V(8)
	for i := uint64(0); i <= cap; i++ {
		call()
		now = now.Add(time.Minute)
	}
	currency := [2]uint64{d.Default.V(12), d.Default.V(11)}
	if money.balances[currency] != cap {
		t.Fatal("daily interaction rewards exceeded cap")
	}
	now = now.Add(72 * time.Hour)
	out := call()
	if val(out, 2) == 0 || val(out, 2) > cap {
		t.Fatal("daily interaction limit did not reset")
	}
	if money.balances[currency] > 2*cap {
		t.Fatal("missed days generated retroactive rewards")
	}
}

// Login never pays an unfinished introduction: it belongs to the first entry
// into the cafeteria game card. A finished one only regains a spent ticket.
func TestCafeteriaLoginOnlyRestoresTheTicketOfAFinishedIntroduction(t *testing.T) {
	d := liveDesign(t)
	store := stateio.NewMemory()
	ctx := command.Context{Identity: command.Identity{SessionID: "introduction"}, State: store}
	money := &ledger{balances: map[[2]uint64]uint64{}}
	intro := [2]uint64{d.Default.V(31), d.Default.V(30)}
	const ticket = 3007 // ContentOpenTable group19/detail1 (dating) ticket in current GameData
	held := false
	open := func() *Service {
		t.Helper()
		s, e := Open(ctx, stateio.RootStore{}, d, money)
		if e != nil {
			t.Fatal(e)
		}
		s.AttachDatingTicket(ticket, func(command.Context, uint64) bool { return held })
		s.AttachOwnedCostumes(func() []uint64 { return []uint64{d.Costumes.Groups[0][0]} })
		s.AttachDatingHasEpisode(func(uint64) bool { return true })
		return s
	}
	if e := open().ReconcileIntroduction(ctx); e != nil {
		t.Fatal(e)
	}
	if money.balances[intro] != 0 || money.balances[[2]uint64{19, ticket}] != 0 {
		t.Fatalf("login paid an unfinished introduction: introduction %d ticket %d", money.balances[intro], money.balances[[2]uint64{19, ticket}])
	}
	if _, _, _, e := open().Handle(ctx, "/CafeteriaIntroductionStoryReward", wire.AppendVarint(nil, 1, 1)); e != nil {
		t.Fatal(e)
	}
	if money.balances[intro] != d.Default.V(29) {
		t.Fatalf("introduction reward %d, want %d", money.balances[intro], d.Default.V(29))
	}
	// The ledger does not expand the box, so the ticket counts as spent.
	if e := open().ReconcileIntroduction(ctx); e != nil {
		t.Fatal(e)
	}
	if money.balances[intro] != d.Default.V(29) || money.balances[[2]uint64{19, ticket}] != 1 {
		t.Fatalf("finished introduction login: introduction %d ticket %d, want %d and 1", money.balances[intro], money.balances[[2]uint64{19, ticket}], d.Default.V(29))
	}
}
