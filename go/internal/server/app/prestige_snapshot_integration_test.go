//go:build integration

package app

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/wire"
)

// GameData 20261021182514 maps prestige4091 to costume4001. Native ItemInfo
// calls AddPrestigeSkinInfo without clearing its cache; PrestigeSkinInfo clears
// that cache and restores IsSet. Historical mission and mail grants can coexist.
func TestPrestigeLoginSnapshotPreservesSelectionWithHistoricalDuplicateItems(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	s := &trapPlayerScenario{factory: factory, account: accounts[0], owner: openNicknamePlayer(t, factory, accounts[0])}
	t.Cleanup(func() { closeNicknamePlayer(t, s.owner) })
	var skin assets.Item
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		items, err := p.ownedItems.GrantOnce(ctx, "historical-mission-skin", []gamedata.BattleReward{{Type: 45, ID: 4091, Count: 1}})
		if err != nil || len(items) != 1 || items[0].Type != 45 || items[0].Count != 1 {
			t.Fatalf("initial skin acquisition: %v %v", items, err)
		}
		skin = items[0]
		info := appScalar(1, 4001)
		info = append(info, appScalar(2, 4091)...)
		info = append(info, appScalar(3, 1)...)
		info = append(info, appScalar(4, skin.TimeValue)...)
		request := wire.AppendBytes(appScalar(1, 1), 2, info)
		if _, _, handled, err := p.eventEconomy.Handle(ctx, "/PrestigeSkinSet", request); !handled || err != nil {
			t.Fatalf("select owned skin: handled=%v error=%v", handled, err)
		}
		duplicate := skin
		duplicate.InvenIndex, err = ctx.NewInstanceID()
		if err != nil {
			t.Fatal(err)
		}
		duplicate.TimeValue++
		raw, err := json.Marshal(duplicate)
		if err != nil {
			t.Fatal(err)
		}
		if err := ctx.State.PutEntry("items", "items", strconv.FormatUint(duplicate.InvenIndex, 10), raw); err != nil {
			t.Fatal(err)
		}
	})
	s.reopen(t)
	s.run(t, false, func(ctx command.Context, p *playerAssembly) {
		skinCount := 0
		for _, item := range p.ownedItems.All(ctx) {
			if item.Type == 45 && item.ID == 4091 {
				skinCount++
			}
		}
		if skinCount != 2 {
			t.Fatalf("historical duplicate fixture was not reopened: %d", skinCount)
		}
		var previous []byte
		for i := range 2 {
			_, snapshot, handled, err := p.eventEconomy.Handle(ctx, "/PrestigeSkinInfo", appScalar(1, uint64(i+2)))
			if err != nil || !handled {
				t.Fatalf("skin snapshot: handled=%v error=%v", handled, err)
			}
			rows := appRows(snapshot, 1)
			if len(rows) != 1 || appValue(rows[0], 1) != 4001 || appValue(rows[0], 2) != 4091 || appValue(rows[0], 3) != 1 || appValue(rows[0], 4) != skin.TimeValue {
				t.Fatalf("snapshot lost unique ownership, selection, or acquisition time: %x", snapshot)
			}
			if i > 0 && !bytes.Equal(previous, snapshot) {
				t.Fatal("repeated snapshot changed skin state")
			}
			previous = snapshot
			_, inventory, handled, err := p.ownedItems.Handle(ctx, "/ItemInfo", appScalar(1, uint64(i+4)))
			if err != nil || !handled {
				t.Fatalf("inventory snapshot: handled=%v error=%v", handled, err)
			}
			ordinaryIllustration := false
			for _, row := range appRows(inventory, 1) {
				if appValue(row, 3) == 45 {
					t.Fatal("inventory snapshot replays a prestige acquisition")
				}
				if appValue(row, 3) == 25 && appValue(row, 2) == 181 {
					ordinaryIllustration = true
				}
			}
			if !ordinaryIllustration {
				t.Fatal("inventory snapshot lost ordinary lobby ownership")
			}
		}
	})
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		items, err := p.ownedItems.GrantOnce(ctx, "another-mail-same-skin", []gamedata.BattleReward{{Type: 45, ID: 4091, Count: 1}})
		if err != nil || len(items) != 0 {
			t.Fatalf("owned skin replayed acquisition: %v %v", items, err)
		}
		items, err = p.ownedItems.GrantOnce(ctx, "new-mail-skin", []gamedata.BattleReward{{Type: 45, ID: 3892, Count: 2}, {Type: 45, ID: 3892, Count: 1}})
		if err != nil {
			t.Fatal(err)
		}
		skins := 0
		for _, item := range items {
			if item.Type == 45 {
				skins++
				if item.ID != 3892 || item.Count != 1 {
					t.Fatalf("invalid unique acquisition: %+v", item)
				}
			}
		}
		if skins != 1 {
			t.Fatalf("new skin must emit one acquisition: %v", items)
		}
		if retry, err := p.ownedItems.GrantOnce(ctx, "new-mail-skin", []gamedata.BattleReward{{Type: 45, ID: 3892, Count: 2}, {Type: 45, ID: 3892, Count: 1}}); err != nil || len(retry) != 0 {
			t.Fatalf("retry replayed acquisition: %v %v", retry, err)
		}
	})
	s.reopen(t)
	s.run(t, false, func(ctx command.Context, p *playerAssembly) {
		skinCount := 0
		for _, item := range p.ownedItems.All(ctx) {
			if item.Type == 45 && item.ID == 3892 {
				skinCount++
				if item.Count != 1 {
					t.Fatalf("persisted skin count is %d", item.Count)
				}
			}
		}
		if skinCount != 1 {
			t.Fatalf("new skin ownership was not persisted once: %d", skinCount)
		}
	})
}
