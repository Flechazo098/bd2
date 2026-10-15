//go:build integration

package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/runtime/player"
	"testing"
)

func TestAllRounderPurchaseChargesNativeMileageAndRollsBackWithBatch(t *testing.T) {
	g := newInventoryGameplay(t)
	var index, beforeLevel, beforeMileage uint64
	inventorySetup(t, g, func(ctx command.Context, p *playerAssembly) {
		for _, c := range p.collection.Costumes() {
			rule := p.design.rosterProgress.Costumes[c.ID]
			if c.Level < rule.Maximum && (index == 0 || c.InvenIndex < index) {
				index = c.InvenIndex
				beforeLevel = c.Level
			}
		}
		if index == 0 {
			t.Fatal("starter has no upgradable owned costume")
		}
		if e := p.wallet.ExchangeOnce(ctx, "all-rounder-test-mileage", nil, []gamedata.Reward{{Type: 20, Count: 4000}}); e != nil {
			t.Fatal(e)
		}
		beforeMileage = p.wallet.Snapshot(ctx).Mileage
	})
	// Current product600001/23 is the monthly one-time selection upgrade; its native price is1600 mileage.
	fields := append(appScalar(2, index), appScalar(3, 600001)...)
	fields = append(fields, appScalar(4, 23)...)
	fields = append(fields, appMessage(5, inventoryItem(0, 0, 20, 1600))...)
	failed := g.request("/CostumeAllRounderUpgrade", fields)
	failed.Requests = append(failed.Requests, player.Request{Path: "/ItemDump", Body: append(appScalar(1, 900), appMessage(2, inventoryItem(999999, 1, 8, 1))...)})
	g.execute(failed, true)
	g.reopen()
	inventorySetup(t, g, func(ctx command.Context, p *playerAssembly) {
		c, ok := p.collection.CostumeByIndex(index)
		if !ok || c.Level != beforeLevel || p.wallet.Snapshot(ctx).Mileage != beforeMileage {
			t.Fatal("failed batch committed costume upgrade or native mileage")
		}
	})
	buy := g.request("/CostumeAllRounderUpgrade", fields)
	g.execute(buy, false)
	inventorySetup(t, g, func(ctx command.Context, p *playerAssembly) {
		c, ok := p.collection.CostumeByIndex(index)
		if !ok || c.Level != beforeLevel+1 {
			t.Fatalf("all-rounder did not advance exactly one level: %+v", c)
		}
		if got := p.wallet.Snapshot(ctx).Mileage; got != beforeMileage-1600 {
			t.Fatalf("all-rounder native charge=%d want%d", got, beforeMileage-1600)
		}
	})
	g.reopen()
	g.execute(buy, false)
	g.execute(g.request("/CostumeAllRounderUpgrade", fields), true)
	g.reopen()
	inventorySetup(t, g, func(ctx command.Context, p *playerAssembly) {
		c, _ := p.collection.CostumeByIndex(index)
		if c.Level != beforeLevel+1 || p.wallet.Snapshot(ctx).Mileage != beforeMileage-1600 {
			t.Fatal("replay or exhausted native monthly limit duplicated charge/upgrade")
		}
	})
}
