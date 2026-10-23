package life_test

import (
	"bytes"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
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
