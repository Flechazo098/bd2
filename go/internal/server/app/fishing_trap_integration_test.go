//go:build integration

package app

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
)

// LocalText38037 sells every pending fish when auto-sale is selected. The
// current fish1 trap price30 and endpoint size premiums2 yield60coins per fish.
func TestFishingTrapAutoSaleUsesNativeCurrencyAndAtomicSettlement(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	g := newSquareGameplay(t, factory, accounts[0])
	inventorySetup(t, g, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.eventEconomy.Apply(ctx, "trap-boat-budget", nil, []gamedata.Reward{{Type: 54, Count: 1000}}); err != nil {
			t.Fatal(err)
		}
	})
	g.call("/FishingBoatUpgrade", nil)
	var fullCount uint64
	inventorySetup(t, g, func(ctx command.Context, p *playerAssembly) {
		raw, found, err := ctx.State.LoadEntry("fishing", "state", "core")
		if err != nil || !found {
			t.Fatal("missing fishing core", err)
		}
		var saved map[string]json.RawMessage
		if err = json.Unmarshal(raw, &saved); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(saved["FishSlots"], &fullCount); err != nil || fullCount == 0 {
			t.Fatal("invalid fishing capacity", err)
		}
		var next uint64
		if err = json.Unmarshal(saved["Next"], &next); err != nil {
			t.Fatal(err)
		}
		now := time.Now().Unix()
		fish := make([]map[string]uint64, fullCount)
		for i := range fish {
			fish[i] = map[string]uint64{"Index": next + uint64(i), "ID": 1, "Count": 1, "Size": 250, "Time": uint64(now - 10000)}
		}
		saved["Fish"], err = json.Marshal(fish)
		if err != nil {
			t.Fatal(err)
		}
		saved["Next"], err = json.Marshal(next + fullCount)
		if err != nil {
			t.Fatal(err)
		}
		saved["Collection"], err = json.Marshal(map[string]map[string]int64{"1": {"Min": 250, "Max": 750, "Created": now - 10000}})
		if err != nil {
			t.Fatal(err)
		}
		raw, err = json.Marshal(saved)
		if err != nil {
			t.Fatal(err)
		}
		if err = ctx.State.PutEntry("fishing", "state", "core", raw); err != nil {
			t.Fatal(err)
		}
		raw, err = json.Marshal(map[string]any{"Version": 1, "Receipt": now - 7200, "Cursor": now, "Fish": []map[string]uint64{{"ID": 1, "Size": 250}, {"ID": 1, "Size": 750}}})
		if err != nil {
			t.Fatal(err)
		}
		if err = ctx.State.PutEntry("fishing", "state", "trap", raw); err != nil {
			t.Fatal(err)
		}
	})
	g.reopen()
	before := squareBalance(t, g, 54, 0)
	if body := g.call("/FishingTrapInfo", nil); len(appRows(body, 2)) != 2 || appValue(body, 3) != 6 {
		t.Fatal("saved pending trap did not resume")
	}
	g.execute(g.request("/FishingTrapCumulativeReward", nil), true)
	if len(appRows(g.call("/FishingTrapInfo", nil), 2)) != 2 || squareBalance(t, g, 54, 0) != before {
		t.Fatal("full-backpack rejection lost pending fish or credited currency")
	}
	failed := g.request("/FishingTrapCumulativeReward", appScalar(2, 1))
	bad := g.request("/ItemDump", appMessage(2, inventoryItem(999999, 1, 8, 1)))
	failed.Requests = append(failed.Requests, bad.Requests...)
	g.execute(failed, true)
	g.reopen()
	if len(appRows(g.call("/FishingTrapInfo", nil), 2)) != 2 || squareBalance(t, g, 54, 0) != before {
		t.Fatal("aborted sale consumed pending fish or credited coins")
	}
	claim := g.request("/FishingTrapCumulativeReward", appScalar(2, 1))
	body := g.execute(claim, false)
	if appValue(body, 5) != 6 || squareBalance(t, g, 54, 0) != before+120 {
		t.Fatal("trap auto-sale did not credit authored fishing coins/experience")
	}
	if uint64(len(appRows(g.call("/FishingItemInfo", nil), 1))) != fullCount {
		t.Fatal("auto-sale modified backpack fish")
	}
	info := appRows(g.call("/FishingInfo", nil), 1)
	if len(info) != 1 || appValue(info[0], 1) != 6 {
		t.Fatal("trap experience was only returned and not saved")
	}
	g.reopen()
	if replay := g.execute(claim, false); !bytes.Equal(replay, body) {
		t.Fatal("auto-sale replay changed")
	}
	if squareBalance(t, g, 54, 0) != before+120 || len(appRows(g.call("/FishingTrapInfo", nil), 2)) != 0 {
		t.Fatal("reopened sale duplicated currency or pending fish")
	}
	if g.owner.assembly.fishingService.Profile().Exp != 6 {
		t.Fatal("reopened fishing experience lost")
	}
}
