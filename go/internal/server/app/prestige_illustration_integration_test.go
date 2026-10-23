//go:build integration

package app

import (
	"bytes"
	"context"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

// Current GameData links prestige 65193 to lobby illustration274 and prestige
// 3892 to135. Ordinary illustration32 is a dating reward, not a costume grant.
// Native MenuUI builds its interaction button from ItemInfo's Type25 rows.
func TestOwnedPrestigeUnlocksInteractionOnLoginAndWhenClaimed(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	s := &trapPlayerScenario{factory: factory, account: accounts[0], owner: openNicknamePlayer(t, factory, accounts[0])}
	var sourceTime uint64
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if err := p.ownedItems.AttachPrestigeIllustrations(ctx, nil); err != nil {
			t.Fatal(err)
		}
		items, err := p.ownedItems.GrantOnce(ctx, "historical-prestige", []gamedata.BattleReward{{Type: 45, ID: 65193, Count: 1}})
		if err != nil || len(items) != 1 {
			t.Fatalf("seed historical external ownership: %v", err)
		}
		sourceTime = items[0].TimeValue
		if _, err := p.collection.GrantCostumes(ctx, "ordinary-costume", []uint64{3303}, p.design.rewardCostumes); err != nil {
			t.Fatal(err)
		}
		if err := p.mailService.EnqueueCompensation(ctx, "new-prestige", "Interaction test", "Claim a new prestige skin", []gamedata.Reward{{Type: 45, ID: 3892, Count: 1}}, time.Now()); err != nil {
			t.Fatal(err)
		}
	})
	closeNicknamePlayer(t, s.owner)
	registry := newPlayerRegistry(factory, time.Minute)
	t.Cleanup(func() {
		if err := registry.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	a, b := friendHTTPLogin(t, registry, accounts[0]), friendHTTPLogin(t, registry, accounts[1])
	find := func(rows [][]byte, typ, id uint64) []byte {
		t.Helper()
		var result []byte
		for _, row := range rows {
			if appValue(row, 3) == typ && appValue(row, 2) == id {
				if result != nil {
					t.Fatalf("duplicate native inventory row for %d/%d", typ, id)
				}
				result = row
			}
		}
		return result
	}
	items := appRows(a.send("/ItemInfo", nil, 21, 0), 1)
	illustration := find(items, 25, 274)
	if illustration == nil || appValue(illustration, 1) == 0 || appValue(illustration, 4) != 1 || appValue(illustration, 6) != sourceTime {
		t.Fatal("historical prestige ownership did not restore a selectable interactive illustration")
	}
	if find(items, 25, 32) != nil || find(items, 25, 135) != nil {
		t.Fatal("login bypassed dating progression or unlocked an unclaimed prestige skin")
	}
	if find(appRows(b.send("/ItemInfo", nil, 21, 0), 1), 25, 274) != nil {
		t.Fatal("another player's prestige unlocked this player's interaction entry")
	}
	var mailID uint64
	for _, row := range appRows(a.send("/MailInfo", nil, 131, 0), 1) {
		title, _, err := wire.Bytes(row, 5)
		if err != nil {
			t.Fatal(err)
		}
		if string(title) == "Interaction test" {
			mailID = appValue(row, 1)
		}
	}
	if mailID == 0 {
		t.Fatal("new prestige claim mail is missing")
	}
	claim := a.send("/MailOpen", appScalar(2, mailID), 132, 0)
	bundles := appRows(claim, 1)
	if len(bundles) != 1 {
		t.Fatal("mail claim did not return the native reward bundle")
	}
	granted := appRows(bundles[0], 1)
	if find(granted, 45, 3892) == nil || find(granted, 25, 135) == nil {
		t.Fatal("claiming prestige did not unlock interaction in the current client session")
	}
	a.seq--
	if retry := a.send("/MailOpen", appScalar(2, mailID), 132, 0); !bytes.Equal(retry, claim) {
		t.Fatal("claim retry changed the associated illustration update")
	}
	if err := registry.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	registry = newPlayerRegistry(factory, time.Minute)
	a = friendHTTPLogin(t, registry, accounts[0])
	items = appRows(a.send("/ItemInfo", nil, 21, 0), 1)
	if !bytes.Equal(find(items, 25, 274), illustration) || find(items, 25, 135) == nil || find(items, 25, 32) != nil {
		t.Fatal("relogin changed illustration identity, lost a claimed interaction, or bypassed dating")
	}
	a.send("/MailOpen", appScalar(2, mailID), 132, 0)
	if rows := appRows(a.send("/ItemInfo", nil, 21, 0), 1); !bytes.Equal(find(rows, 25, 135), find(items, 25, 135)) {
		t.Fatal("reopening claimed mail duplicated the companion illustration")
	}
	if err := registry.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.owner = openNicknamePlayer(t, factory, accounts[0])
	var actualIndex uint64
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		granted, err := p.ownedItems.GrantOnce(ctx, "explicit-illustration", []gamedata.BattleReward{{Type: 25, ID: 274, Count: 1}})
		if err != nil || len(granted) != 1 {
			t.Fatalf("grant separate illustration item: %v", err)
		}
		actualIndex = granted[0].InvenIndex
	})
	closeNicknamePlayer(t, s.owner)
	registry = newPlayerRegistry(factory, time.Minute)
	a = friendHTTPLogin(t, registry, accounts[0])
	if row := find(appRows(a.send("/ItemInfo", nil, 21, 0), 1), 25, 274); row == nil || appValue(row, 1) != actualIndex {
		t.Fatal("an existing illustration item was duplicated or replaced by the skin ownership view")
	}
}
