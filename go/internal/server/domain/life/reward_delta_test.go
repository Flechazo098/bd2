package life_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/life"
	"bd2server/internal/server/protocol/wire"
)

// LifePacket.AddLifeItemInfo/AddLifeFurnishingItemInfo and AddHaveCurrency
// add reward counts. LifeInfo clears those caches before loading full balances.
func TestNativeRewardDeltasRemainConsistentThroughGatherCraftAndRelog(t *testing.T) {
	g := scenario(t)
	g.d.Tables["LifeObjectGroupTable"][[3]uint64{304}] = proto(5, 304, 12, 3, 7, 1)
	g.d.Tables["LifeBuildingObjectTable"][[3]uint64{304}] = proto(4, 304, 2, 11)
	g.d.Tables["LifeObjectGroupTable"][[3]uint64{401}] = proto(5, 401, 12, 1, 7, 1, 9, 10)
	g.d.Tables["LifeCraftingObjectTable"][[3]uint64{401}] = proto(1, 401, 3, 3, 4, 501, 5, 66)
	g.call("/LifeWorldChoice", proto(2, 91))
	g.call("/LifeWorldObjectPlaceSave", child(nil, 2, placement(304, 5, 6)))
	client := map[uint64]uint64{}
	apply := func(bundle []byte) {
		t.Helper()
		for _, it := range repeated(t, bundle, 1) {
			client[scalar(t, it, 2)] += scalar(t, it, 4)
		}
	}
	fund, err := g.s.Apply(g.ctx, "initial", nil, []gamedata.Reward{{Type: 66, ID: 501, Count: 5}, {Type: 64, Count: 10}})
	if err != nil {
		t.Fatal(err)
	}
	apply(fund)
	bonus, err := g.s.Apply(g.ctx, "bonus", nil, []gamedata.Reward{{Type: 64, Count: 3}})
	if err != nil {
		t.Fatal(err)
	}
	apply(bonus)
	if client[0] != 13 {
		t.Fatalf("native life coin cache=%d, want 13", client[0])
	}
	parent := child(proto(6, 301, 1, 2, 2, 3), 10, proto(6, 1, 4, 0, 7, 1))
	gather := child(nil, 2, child(proto(1, 1), 2, parent))
	for range 2 {
		harvest := g.call("/LifeWorldObjectGathering", gather)
		apply(nested(t, harvest, 2))
		g.now = g.now.Add(30 * time.Second)
	}
	if client[501] != 9 {
		t.Fatalf("native gathered material cache=%d, want 9", client[501])
	}
	var materialIndex uint64
	for _, it := range repeated(t, g.call("/LifeInfo", nil), 3) {
		if scalar(t, it, 2) == 501 {
			materialIndex = scalar(t, it, 1)
		}
	}
	if materialIndex == 0 {
		t.Fatal("missing material inventory index")
	}
	for _, count := range []uint64{2, 1} {
		request := child(proto(2, 401, 3, count), 4, proto(1, materialIndex, 2, 501, 3, 66, 4, 3*count))
		g.seq++
		encoded := append(proto(1, g.seq), request...)
		_, out, _, err := g.s.Handle(g.ctx, "/LifeCrafting", encoded)
		if err != nil {
			t.Fatal(err)
		}
		client[501] -= 3 * count
		apply(nested(t, out, 1))
		_, retry, _, err := g.s.Handle(g.ctx, "/LifeCrafting", encoded)
		if err != nil || !bytes.Equal(retry, out) {
			t.Fatalf("craft retry must replay: %v", err)
		}
	}
	if client[401] != 3 || client[501] != 0 {
		t.Fatalf("native cache after crafts: %v", client)
	}
	g.reopen()
	info := g.call("/LifeInfo", nil)
	for _, it := range append(repeated(t, info, 3), repeated(t, info, 4)...) {
		id, count := scalar(t, it, 2), scalar(t, it, 4)
		if client[id] != count {
			t.Fatalf("relogin inventory item %d=%d, native cache=%d", id, count, client[id])
		}
	}
	if got := scalar(t, nested(t, info, 2), 1); got != client[0] {
		t.Fatalf("relogin coin=%d, native cache=%d", got, client[0])
	}
	if balance, err := g.s.Balance(g.ctx, 66, 501); err != nil || balance != 0 {
		t.Fatalf("materials not spent exactly once: %d (%v)", balance, err)
	}
	if balance, err := g.s.Balance(g.ctx, 65, 401); err != nil || balance != 3 {
		t.Fatalf("craft output not persisted exactly once: %d (%v)", balance, err)
	}
}

// Native food UI sends slider quantities or several selected inventory items.
// Each food extends its timer; separate foods with the same buff add effects.
func TestFoodPurchaseCookingEatingAndBuffLifetime(t *testing.T) {
	g := scenario(t)
	for _, id := range []uint64{502, 503} {
		g.d.Tables["LifeItemTable"][[3]uint64{id}] = proto(5, id, 1, 601, 8, 1000, 9, 3)
		g.d.Tables["LifeBuyItemTable"][[3]uint64{7, id}] = proto(1, 7, 2, id, 3, 1, 4, id, 5, 66, 6, 2, 8, 64)
	}
	g.d.Tables["LifeBuffTable"][[3]uint64{601}] = wire.AppendDouble(proto(8, 601, 1, 600, 4, 12), 5, 1)
	g.d.Tables["LifeObjectGroupTable"][[3]uint64{304}] = proto(5, 304, 12, 3, 7, 1)
	g.d.Tables["LifeBuildingObjectTable"][[3]uint64{304}] = proto(4, 304, 2, 13)
	g.d.Tables["LifeCookTable"][[3]uint64{1}] = proto(1, 1, 2, 2, 3, 10, 4, 502)
	g.call("/LifeWorldChoice", proto(2, 91))
	g.fund("budget", 100, 2)
	g.call("/LifeWorldObjectPlaceSave", child(nil, 2, placement(304, 5, 6)))
	for _, id := range []uint64{502, 503} {
		g.call("/LifeShopBuy", proto(2, 7, 3, id, 4, 5))
	}
	inventory := map[uint64]uint64{}
	for _, it := range repeated(t, g.call("/LifeInfo", nil), 3) {
		inventory[scalar(t, it, 2)] = scalar(t, it, 1)
	}
	g.call("/LifeCooking", child(proto(2, 1, 3, 1), 4, proto(1, inventory[501], 2, 501, 3, 66, 4, 2)))
	if coin, _ := g.s.Coin(g.ctx); coin != 80 {
		t.Fatalf("purchase cost=%d, want balance 80", coin)
	}
	if balance, _ := g.s.Balance(g.ctx, 66, 502); balance != 6 {
		t.Fatalf("purchase + cooking=%d, want 6", balance)
	}
	use := func(id, count uint64) []byte { return proto(1, inventory[id], 2, id, 3, 66, 4, count) }
	firstTime := g.now
	first := g.call("/LifeEatFood", child(nil, 2, use(502, 2)))
	if got := scalar(t, nested(t, first, 1), 2); got != uint64(firstTime.Add(20*time.Minute).UnixMilli()) {
		t.Fatalf("two servings deadline=%d", got)
	}
	g.now = g.now.Add(time.Minute)
	request := child(child(nil, 2, use(502, 1)), 2, use(503, 1))
	g.seq++
	encoded := append(proto(1, g.seq), request...)
	_, out, _, err := g.s.Handle(g.ctx, "/LifeEatFood", encoded)
	if err != nil {
		t.Fatal(err)
	}
	_, retry, _, err := g.s.Handle(g.ctx, "/LifeEatFood", encoded)
	if err != nil || !bytes.Equal(out, retry) {
		t.Fatalf("food retry must replay without consuming or extending again: %v", err)
	}
	if balance, _ := g.s.Balance(g.ctx, 66, 502); balance != 3 {
		t.Fatalf("food debit=%d, want 3", balance)
	}
	if balance, _ := g.s.Balance(g.ctx, 66, 503); balance != 4 {
		t.Fatalf("batch food debit=%d, want 4", balance)
	}
	deadlines := map[uint64]uint64{}
	for _, f := range repeated(t, out, 1) {
		deadlines[scalar(t, f, 1)] = scalar(t, f, 2)
	}
	if len(deadlines) != 2 || deadlines[502] != uint64(firstTime.Add(30*time.Minute).UnixMilli()) || deadlines[503] != uint64(g.now.Add(10*time.Minute).UnixMilli()) {
		t.Fatalf("batch deadlines=%v", deadlines)
	}
	g.reopen()
	g.ctx.SessionID = "food-relogin"
	restored := repeated(t, g.call("/LifeInfo", nil), 6)
	if len(restored) != 2 {
		t.Fatalf("restored food effects=%d", len(restored))
	}
	for _, f := range restored {
		if scalar(t, f, 2) != deadlines[scalar(t, f, 1)] {
			t.Fatal("relogin changed food deadline")
		}
	}
	gather := child(nil, 2, child(proto(1, 1), 2, child(proto(1, 2, 2, 3, 6, 301), 10, proto(4, 0, 6, 1))))
	harvest := func(want uint64) {
		t.Helper()
		bundle := nested(t, g.call("/LifeWorldObjectGathering", gather), 2)
		items := repeated(t, bundle, 1)
		if len(items) != 1 || scalar(t, items[0], 4) != want {
			t.Fatalf("gathering food effect: got %x, want %d", bundle, want)
		}
	}
	harvest(4) // Base 2 + one modifier per food, regardless of servings.
	g.now = firstTime.Add(11 * time.Minute)
	g.reopen()
	if got := len(repeated(t, g.call("/LifeInfo", nil), 6)); got != 1 {
		t.Fatalf("expired food remains active: %d", got)
	}
	harvest(3)
	g.now = firstTime.Add(30 * time.Minute)
	g.reopen()
	if got := len(repeated(t, g.call("/LifeInfo", nil), 6)); got != 0 {
		t.Fatalf("expired food restored: %d", got)
	}
	harvest(2)
	again := g.call("/LifeEatFood", child(nil, 2, use(503, 1)))
	if scalar(t, nested(t, again, 1), 2) != uint64(g.now.Add(10*time.Minute).UnixMilli()) {
		t.Fatal("expired food did not restart from now")
	}
	before := g.call("/LifeInfo", nil)
	for _, bad := range [][]byte{use(503, 0), use(503, 99), proto(1, inventory[503]+100, 2, 503, 3, 66, 4, 1), use(501, 1)} {
		_, err := g.request("/LifeEatFood", child(child(nil, 2, use(502, 1)), 2, bad))
		if !errors.Is(err, life.ErrInvalid) && !errors.Is(err, life.ErrFunds) {
			t.Fatalf("invalid selection accepted: %v", err)
		}
		if after := g.call("/LifeInfo", nil); !bytes.Equal(before, after) {
			t.Fatal("failed batch consumed inventory or changed timers")
		}
	}
	duplicate := g.call("/LifeEatFood", child(child(nil, 2, use(502, 1)), 2, use(502, 1)))
	if len(repeated(t, duplicate, 1)) != 1 || scalar(t, nested(t, duplicate, 1), 2) != uint64(g.now.Add(20*time.Minute).UnixMilli()) {
		t.Fatal("duplicate food lines must consume and extend their combined quantity once")
	}
	g.reopen()
	if balance, err := g.s.Balance(g.ctx, 66, 502); err != nil || balance != 1 {
		t.Fatalf("duplicate food lines persisted balance=%d, want 1: %v", balance, err)
	}
}
