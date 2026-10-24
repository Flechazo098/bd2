package life_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/life"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

// This deliberately small economy is a gameplay scenario, independent of the
// installed IDs/prices. Field numbers come from the extracted 2.36.7 Proto;
// worker quantity/cycle and the settlement cap come from PackInfoLife.cs and
// LifePacket.CanSettlementHelperReward. No production encoder is used here.
func proto(values ...uint64) []byte {
	var b []byte
	for i := 0; i < len(values); i += 2 {
		b = wire.AppendVarint(b, int(values[i]), values[i+1])
	}
	return b
}
func child(b []byte, field int, value []byte) []byte { return wire.AppendBytes(b, field, value) }
func scalar(t *testing.T, b []byte, field int) uint64 {
	t.Helper()
	v, _, err := wire.Varint(b, field)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func nested(t *testing.T, b []byte, field int) []byte {
	t.Helper()
	v, ok, err := wire.Bytes(b, field)
	if err != nil || !ok {
		t.Fatalf("missing message field %d: %x (%v)", field, b, err)
	}
	return v
}
func repeated(t *testing.T, b []byte, field int) [][]byte {
	t.Helper()
	var out [][]byte
	if err := wire.Walk(b, func(f wire.Field) error {
		if f.Number == field && f.Type == 2 {
			out = append(out, f.Value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
func catalog(t *testing.T) *gamedata.LifeCatalog {
	t.Helper()
	tables := map[string][][]byte{
		"LifeDefaultTable":            {proto(21, 0, 4, 5, 18, 2, 19, 7, 20, 10, 26, 25)},
		"LifeWorldTable":              {proto(4, 91, 1, 1), proto(4, 92, 1, 1)},
		"LifeDefaultObjectGroupTable": {proto(3, 1, 8, 91, 1, 1, 2, 301, 4, 3, 5, 2, 6, 3), proto(3, 2, 8, 92, 1, 1, 2, 301, 4, 3, 5, 2, 6, 3)},
		"LifeObjectGroupTable":        {proto(5, 301, 12, 3, 7, 1), proto(5, 302, 12, 3, 7, 1, 9, 1), proto(5, 303, 12, 3, 7, 1, 9, 1)},
		"LifeBuildingObjectTable":     {proto(4, 301, 2, 1, 3, 30, 5, 1, 7, 1), proto(4, 302, 2, 102, 11, 5, 8, 3, 9, 501, 10, 66), proto(4, 303, 2, 101, 11, 5, 8, 2, 9, 501, 10, 66)},
		"LifeGatheringObjectTable":    {proto(4, 301, 6, 1, 10, 1, 1, 3, 2, 1, 3, 100, 8, 2, 9, 10)},
		"LifeItemTable":               {proto(5, 501, 10, 10, 3, 1, 8, 1000)},
		"LifeHelperSpeciesTable":      {proto(9, 71, 10, 1, 1, 801, 2, 2, 3, 4, 4, 3, 6, 901, 8, 902)},
		"LifeCharGrowthTable":         {proto(2, 1, 3, 1, 4, 3), proto(2, 1, 3, 2, 4, 9), proto(2, 2, 3, 1, 4, 5), proto(2, 3, 3, 1, 4, 5)},
		"LifeBuyItemTable":            {proto(1, 6, 2, 8, 3, 2, 4, 501, 5, 66, 6, 3, 8, 64, 9, 2, 10, 1)},
	}
	d, err := gamedata.NewLifeCatalog(tables)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

type game struct {
	t       *testing.T
	ctx     command.Context
	s       *life.Service
	d       *gamedata.LifeCatalog
	now     time.Time
	seq     uint64
	options life.Options
}

func scenario(t *testing.T) *game {
	t.Helper()
	g := &game{t: t, ctx: command.Context{Identity: command.Identity{AccountID: "player", SessionID: "session"}, State: stateio.NewMemory()}, d: catalog(t), now: time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)}
	g.options = life.Options{Now: func() time.Time { return g.now }, Sample: func(uint64) (uint64, error) { return 0, nil }, ValidateAvatar: func(_ command.Context, b []byte) error {
		if scalar(t, b, 1) != 801 {
			return errors.New("not owned")
		}
		return nil
	}}
	g.reopen()
	return g
}
func (g *game) reopen() {
	g.t.Helper()
	var err error
	g.s, err = life.Open(g.ctx, g.d, g.options)
	if err != nil {
		g.t.Fatal(err)
	}
}
func (g *game) request(path string, b []byte) ([]byte, error) {
	g.t.Helper()
	g.seq++
	b = append(proto(1, g.seq), b...)
	_, out, handled, err := g.s.Handle(g.ctx, path, b)
	if !handled {
		g.t.Fatalf("unhandled %s", path)
	}
	return out, err
}
func (g *game) call(path string, b []byte) []byte {
	g.t.Helper()
	out, err := g.request(path, b)
	if err != nil {
		g.t.Fatalf("%s: %v", path, err)
	}
	return out
}
func (g *game) fund(key string, coin, wood uint64) {
	g.t.Helper()
	var rewards []gamedata.Reward
	if coin > 0 {
		rewards = append(rewards, gamedata.Reward{Type: 64, Count: coin})
	}
	if wood > 0 {
		rewards = append(rewards, gamedata.Reward{Type: 66, ID: 501, Count: wood})
	}
	if _, err := g.s.Apply(g.ctx, key, nil, rewards); err != nil {
		g.t.Fatal(err)
	}
}
func placement(id, x, y uint64) []byte { return child(proto(1, 1), 2, proto(6, id, 1, x, 2, y)) }
func (g *game) build(id, x, y uint64) []byte {
	g.t.Helper()
	p := placement(id, x, y)
	g.call("/LifeWorldObjectPlaceSave", child(nil, 2, p))
	if _, err := g.request("/LifeWorldObjectBuildCompleted", child(nil, 2, p)); !errors.Is(err, life.ErrInvalid) {
		g.t.Fatalf("early completion: %v", err)
	}
	g.now = g.now.Add(5 * time.Second)
	return nested(g.t, g.call("/LifeWorldObjectBuildCompleted", child(nil, 2, p)), 1)
}

func TestResidentsWorkersGrowthAndCurrencySurviveRealActionFlow(t *testing.T) {
	g := scenario(t)
	initial := g.call("/LifeUserInfo", nil)
	user := nested(t, initial, 2)
	if scalar(t, user, 2) != 0 || scalar(t, nested(t, user, 3), 1) != 1 {
		t.Fatal("new player must have real unselected world and initial growth")
	}
	g.call("/LifeWorldChoice", proto(2, 91))
	g.fund("mail-grant", 20, 8)
	house := g.build(302, 5, 6)
	avatar := proto(1, 801, 2, 901)
	citizen := nested(t, g.call("/LifeCitizenRecruit", child(child(nil, 2, house), 3, child(nil, 3, avatar))), 1)
	workerHouse := g.build(303, 8, 9)
	candidate := repeated(t, g.call("/LifeHelperGacha", child(nil, 2, workerHouse)), 1)[0]
	recruit := child(child(nil, 3, candidate), 4, workerHouse)
	g.call("/LifeHelperRecruit", recruit)
	helper := child(proto(1, scalar(t, candidate, 1), 6, 1), 5, avatar)
	assigned := g.call("/LifeHelperAssign", child(child(nil, 2, helper), 3, workerHouse))
	if got := scalar(t, nested(t, assigned, 1), 8); got != uint64(g.now.UnixMilli()) {
		t.Fatalf("native helper assignment timestamp: got %d, want %d milliseconds", got, g.now.UnixMilli())
	}
	g.now = g.now.Add(20 * time.Second)
	preview := g.call("/LifeHelperRewardInfo", nil)
	if got := scalar(t, preview, 1); got != uint64(g.now.Add(-20*time.Second).UnixMilli()) {
		t.Fatalf("native helper preview timestamp: got %d", got)
	}
	previewItems := repeated(t, nested(t, preview, 2), 1)
	if len(previewItems) != 1 || scalar(t, previewItems[0], 4) != 8 {
		t.Fatalf("two work cycles at four per cycle: %x", preview)
	}
	before, _ := g.s.Balance(g.ctx, 66, 501)
	g.call("/LifeHelperRewardInfo", nil)
	after, _ := g.s.Balance(g.ctx, 66, 501)
	if before != after {
		t.Fatal("reward preview granted inventory")
	}
	g.seq++
	claim := proto(1, g.seq)
	_, first, _, err := g.s.Handle(g.ctx, "/LifeHelperReward", claim)
	if err != nil {
		t.Fatal(err)
	}
	_, again, _, err := g.s.Handle(g.ctx, "/LifeHelperReward", claim)
	if err != nil || !bytes.Equal(first, again) {
		t.Fatal("identical settlement retry must replay")
	}
	g.reopen()
	g.ctx.SessionID = "relogin"
	restored := g.call("/LifeUserInfo", nil)
	if len(repeated(t, restored, 3)) != 1 || !bytes.Equal(nested(t, restored, 3), citizen) || len(repeated(t, restored, 4)) != 1 {
		t.Fatalf("lost residents or helpers: %x", restored)
	}
	if coin, _ := g.s.Coin(g.ctx); coin != 13 {
		t.Fatalf("hire did not consume the persisted mail currency: %d", coin)
	}
	balance, _ := g.s.Balance(g.ctx, 66, 501)
	if balance != 11 {
		t.Fatalf("materials: want initial 8 - building 5 + helper 8, got %d", balance)
	}
	forest := placement(301, 2, 3)
	forestObject := nested(t, forest, 2)
	forestObject = child(forestObject, 10, proto(6, 1, 4, 0, 7, 999))
	gather := child(nil, 2, child(proto(1, 1), 2, forestObject))
	g.call("/LifeWorldObjectGathering", gather)
	if _, err := g.request("/LifeWorldObjectGathering", gather); err == nil {
		t.Fatal("gathered resource granted twice before regeneration")
	}
	g.reopen()
	current := nested(t, g.call("/LifeUserInfo", nil), 2)
	growth := nested(t, current, 3)
	if scalar(t, growth, 1) != 2 || scalar(t, growth, 2) != 0 {
		t.Fatalf("earned experience did not advance level across relog: %x", growth)
	}
	g.now = g.now.Add(30 * time.Second)
	g.call("/LifeWorldObjectGathering", gather)
	g.call("/LifeWorldChoice", proto(2, 92))
	reset := g.call("/LifeUserInfo", nil)
	if len(repeated(t, reset, 3)) != 0 || scalar(t, nested(t, reset, 4), 3) != 0 {
		t.Fatal("terrain reset did not release residents/helpers")
	}
	if scalar(t, nested(t, nested(t, reset, 2), 3), 1) != 2 {
		t.Fatal("terrain reset erased earned growth")
	}
}

func TestShopLimitResetAndRejectedOperationsLeaveCurrencyUntouched(t *testing.T) {
	g := scenario(t)
	g.fund("mail", 20, 0)
	g.call("/LifeShopBuy", proto(2, 6, 3, 8, 4, 2))
	if _, err := g.request("/LifeShopBuy", proto(2, 6, 3, 8, 4, 1)); err == nil {
		t.Fatal("daily purchase limit bypassed")
	}
	if coin, _ := g.s.Coin(g.ctx); coin != 14 {
		t.Fatal("rejected purchase charged currency")
	}
	g.reopen()
	if info := g.call("/LifeShopBuyInfo", nil); scalar(t, nested(t, info, 1), 3) != 2 {
		t.Fatal("purchase count lost on reload")
	}
	g.now = g.now.Add(24 * time.Hour)
	g.call("/LifeShopBuy", proto(2, 6, 3, 8, 4, 1))
	if coin, _ := g.s.Coin(g.ctx); coin != 11 {
		t.Fatal("daily reset did not reopen purchase")
	}
	if err := g.s.CanApply(g.ctx, []gamedata.Reward{{Type: 64, Count: 50}}, nil); !errors.Is(err, life.ErrFunds) {
		t.Fatalf("CanApply funds: %v", err)
	}
	if coin, _ := g.s.Coin(g.ctx); coin != 11 {
		t.Fatal("validation changed funds")
	}
	before, _ := g.s.Items(g.ctx)
	if _, err := g.s.Apply(g.ctx, "bad", []gamedata.Reward{{Type: 64, Count: 1}}, []gamedata.Reward{{Type: 66, ID: 9999, Count: 1}}); err == nil {
		t.Fatal("invalid reward should abort both sides")
	}
	after, _ := g.s.Items(g.ctx)
	if !bytes.Equal(bytes.Join(before, nil), bytes.Join(after, nil)) {
		t.Fatal("failed economy altered inventory")
	}
	if coin, _ := g.s.Coin(g.ctx); coin != 11 {
		t.Fatal("failed reward retained debit")
	}
}

type failStore struct {
	stateio.AtomicEntryStore
	fail bool
}

func (s *failStore) SaveWithEntries(domain string, core []byte, changes []stateio.EntryMutation) error {
	if s.fail {
		return errors.New("injected atomic write failure")
	}
	return s.AtomicEntryStore.SaveWithEntries(domain, core, changes)
}
func TestAtomicWriteFailureDoesNotLeakCandidateStateOrReceipt(t *testing.T) {
	g := scenario(t)
	store := &failStore{AtomicEntryStore: g.ctx.State}
	g.ctx.State = store
	g.fund("mail", 20, 0)
	store.fail = true
	req := proto(1, 7, 2, 6, 3, 8, 4, 1)
	if _, _, _, err := g.s.Handle(g.ctx, "/LifeShopBuy", req); err == nil {
		t.Fatal("write failure swallowed")
	}
	store.fail = false
	g.reopen()
	if coin, _ := g.s.Coin(g.ctx); coin != 20 {
		t.Fatal("uncommitted debit leaked")
	}
	if _, _, _, err := g.s.Handle(g.ctx, "/LifeShopBuy", req); err != nil {
		t.Fatalf("retry after failed commit: %v", err)
	}
	if coin, _ := g.s.Coin(g.ctx); coin != 17 {
		t.Fatal("successful retry must charge once")
	}
}

// TimerManager.UnixTimeStampToDateTime consumes milliseconds for resource,
// construction, food and shop timers; saved life state keeps seconds.
func TestLifeWireTimersMatchNativeMilliseconds(t *testing.T) {
	g := scenario(t)
	g.call("/LifeWorldChoice", proto(2, 91))
	info := g.call("/LifeInfo", nil)
	forest := nested(t, nested(t, info, 1), 2)
	inner := nested(t, forest, 10)
	if got := scalar(t, inner, 8); got != uint64(g.now.UnixMilli()) {
		t.Fatalf("resource creation time: got %d, want %d", got, g.now.UnixMilli())
	}
	gather := child(nil, 2, child(proto(1, 1), 2, child(proto(1, 2, 2, 3, 6, 301), 10, proto(4, 0, 6, 1, 7, 1))))
	harvest := g.call("/LifeWorldObjectGathering", gather)
	spent := nested(t, nested(t, nested(t, harvest, 3), 2), 10)
	deadline := time.UnixMilli(int64(scalar(t, spent, 8))).Add(30 * time.Second)
	if scalar(t, spent, 7) != 0 || !deadline.Equal(g.now.Add(30*time.Second)) {
		t.Fatalf("native resource must remain spent until %s, got %s", g.now.Add(30*time.Second), deadline)
	}
	g.fund("construction", 0, 3)
	building := nested(t, nested(t, g.call("/LifeWorldObjectPlaceSave", child(nil, 2, placement(302, 5, 6))), 1), 2)
	if got := scalar(t, building, 9); got != uint64(g.now.Add(5*time.Second).UnixMilli()) {
		t.Fatalf("native construction deadline: got %d", got)
	}
	g.d.Tables["LifeItemTable"][[3]uint64{502}] = proto(5, 502, 1, 601, 8, 1000, 9, 3)
	g.d.Tables["LifeBuffTable"][[3]uint64{601}] = proto(8, 601, 1, 60)
	if _, err := g.s.Apply(g.ctx, "food", nil, []gamedata.Reward{{Type: 66, ID: 502, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	items, err := g.s.Items(g.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var foodIndex uint64
	for _, it := range items {
		if scalar(t, it, 2) == 502 {
			foodIndex = scalar(t, it, 1)
		}
	}
	food := g.call("/LifeEatFood", child(nil, 2, proto(1, foodIndex, 2, 502, 3, 66, 4, 1)))
	if got := scalar(t, nested(t, food, 1), 2); got != uint64(g.now.Add(time.Minute).UnixMilli()) {
		t.Fatalf("native food deadline: got %d", got)
	}
	g.reopen()
	restoredFood := nested(t, g.call("/LifeInfo", nil), 6)
	if got := scalar(t, restoredFood, 2); got != uint64(g.now.Add(time.Minute).UnixMilli()) {
		t.Fatalf("food deadline changed after reload: %d", got)
	}
	shop := g.call("/LifeShopBuyInfo", nil)
	expected := []time.Time{time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC), time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)}
	for i, want := range expected {
		if got := scalar(t, shop, i+2); got != uint64(want.UnixMilli()) {
			t.Fatalf("shop reset field %d: got %d, want %d", i+2, got, want.UnixMilli())
		}
	}
}
