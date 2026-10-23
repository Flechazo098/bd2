//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/startergift"
)

func TestStarterGiftCurrentSourcesExcludeDefaultsAndAchievements(t *testing.T) {
	factory, _ := newIntegrationFactory(t)
	gifts, err := factory.seeds.starterGifts.Resolve(startergift.Selection{
		Limited: factory.design.limitedCostumes, Prestige: factory.design.prestigeCatalog,
		Avatars: factory.design.avatarProfile, OwnedPrestige: map[uint64]bool{65193: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	var external, linked []gamedata.Reward
	for _, gift := range gifts {
		switch gift.Identity {
		case startergift.LimitedAvatars:
			external = gift.Rewards
		case "starter:prestige-avatar:65193:v1":
			linked = gift.Rewards
		}
	}
	// Current AvatarItemTable unlock texts: April Fools dice, Valentine roulette,
	// artbook code, and coffee-truck voucher. None is a default or title reward.
	want := []gamedata.Reward{{Type: 49, ID: 20018, Count: 1}, {Type: 49, ID: 50007, Count: 1}, {Type: 49, ID: 60015, Count: 1}, {Type: 49, ID: 60018, Count: 1}}
	if !slices.Equal(external, want) {
		t.Fatalf("external gift=%v, want only audited external sources %v", external, want)
	}
	// AvatarSetTable 2001 links PrestigeSkin 65193 to these three pieces.
	want = []gamedata.Reward{{Type: 49, ID: 10025, Count: 1}, {Type: 49, ID: 20033, Count: 1}, {Type: 49, ID: 40048, Count: 1}}
	if !slices.Equal(linked, want) {
		t.Fatalf("owned prestige attachment=%v want %v", linked, want)
	}
	for _, gift := range gifts {
		if startergift.IsAvatarGift(gift.Identity) && gift.Identity != startergift.LimitedAvatars && gift.Identity != "starter:prestige-avatar:65193:v1" {
			t.Fatalf("unowned prestige skin received an outfit: %s", gift.Identity)
		}
		for _, reward := range gift.Rewards {
			if startergift.IsAvatarGift(gift.Identity) && (reward.Type != 49 || slices.Contains([]uint64{10001, 40001, 50013, 50014, 10030, 40055}, reward.ID)) {
				t.Fatalf("default, collaboration body, or title reward was included: %v", reward)
			}
		}
	}
}

func TestStarterGiftUpgradePreservesIssuedMailAndFiltersOwnedAccessories(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	current := factory.seeds.starterGifts
	legacy, err := cloneSeed(&current)
	if err != nil {
		t.Fatal(err)
	}
	for i := range legacy.Gifts {
		legacy.Gifts[i].Parts = nil
	}
	factory.seeds.starterGifts = *legacy
	owner, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	g := &puzzleGameplay{t: t, factory: factory, account: accounts[0], owner: owner}
	t.Cleanup(func() {
		if g.owner != nil {
			if err := g.owner.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	readMail := func() (map[string]uint64, map[string][]byte) {
		t.Helper()
		tx, err := g.owner.repository.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		ids := map[string]uint64{}
		records := map[string][]byte{}
		for _, bucket := range []string{"issued", "dynamic", "history"} {
			rows, err := g.owner.assembly.scope.ListEntries(tx, "mail", bucket)
			if err != nil {
				t.Fatal(err)
			}
			for key, raw := range rows {
				records[bucket+":"+key] = append([]byte(nil), raw...)
				if bucket == "issued" {
					var id uint64
					if err := json.Unmarshal(raw, &id); err != nil {
						t.Fatal(err)
					}
					ids[key] = id
				}
			}
		}
		return ids, records
	}
	ids, _ := readMail()
	if ids[startergift.LimitedCostumes] == 0 || ids[startergift.PrestigeSkins] == 0 || ids[startergift.LimitedAvatars] != 0 {
		t.Fatal("old account fixture must have only the two historical gifts")
	}
	// Leave the limited costume gift unclaimed and claim the prestige gift.
	g.call("/MailOpen", appScalar(2, ids[startergift.PrestigeSkins]))
	_, before := readMail()
	factory.seeds.starterGifts = current
	g.reopen()
	g.call("/MailInfo", nil)
	ids, after := readMail()
	for key, raw := range before {
		if !bytes.Equal(raw, after[key]) {
			t.Fatalf("upgrading the gift catalog changed historical %s", key)
		}
	}
	accessoryMail := ids[startergift.LimitedAvatars]
	if accessoryMail == 0 {
		t.Fatal("existing account did not receive the new external accessory gift")
	}
	// This piece is acquired after delivery; claiming must omit its new-item row.
	scenario := &trapPlayerScenario{factory: factory, owner: g.owner, account: accounts[0]}
	scenario.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.eventEconomy.Apply(ctx, "other-accessory-source", nil, []gamedata.Reward{{Type: 49, ID: 20018, Count: 1}}); err != nil {
			t.Fatal(err)
		}
	})
	request := g.request("/MailOpen", appScalar(2, accessoryMail))
	claim := g.execute(request, false)
	if retry := g.execute(request, false); !bytes.Equal(retry, claim) {
		t.Fatal("same-request retry changed the filtered acquisition bundle")
	}
	for _, bundle := range appRows(claim, 1) {
		for _, row := range appRows(bundle, 1) {
			if appValue(row, 3) == 49 && appValue(row, 2) == 20018 {
				t.Fatal("already-owned accessory was returned as newly acquired")
			}
		}
	}
	for _, id := range []uint64{20018, 50007, 60015, 60018} {
		if g.itemCount(49, id) != 1 {
			t.Fatalf("external accessory %d was missing or duplicated", id)
		}
	}
	reopenedClaim := g.call("/MailOpen", appScalar(2, accessoryMail))
	for _, bundle := range appRows(reopenedClaim, 1) {
		if len(bundle) != 0 {
			t.Fatal("a new request to an opened accessory mail returned an acquisition bundle")
		}
	}
	for _, id := range []uint64{20018, 50007, 60015, 60018} {
		if g.itemCount(49, id) != 1 {
			t.Fatalf("new claim request duplicated external accessory %d", id)
		}
	}
	_, stable := readMail()
	g.reopen()
	g.call("/MailInfo", nil)
	_, reopened := readMail()
	if len(stable) != len(reopened) {
		t.Fatal("relogin issued the same gifts again")
	}
	for key, raw := range stable {
		if !bytes.Equal(raw, reopened[key]) {
			t.Fatalf("relogin changed gift record %s", key)
		}
	}
	for _, id := range []uint64{20018, 50007, 60015, 60018} {
		if g.itemCount(49, id) != 1 {
			t.Fatalf("relogin duplicated or lost external accessory %d", id)
		}
	}
}

func TestNewlyOwnedPrestigeCreatesLinkedGiftOnMailInfoWithoutDuplicatingSharedPieces(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	owner, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	g := &puzzleGameplay{t: t, factory: factory, account: accounts[0], owner: owner}
	t.Cleanup(func() { _ = g.owner.Close() })
	scenario := &trapPlayerScenario{factory: factory, owner: owner, account: accounts[0]}
	var linkedMail uint64
	readLinked := func() {
		t.Helper()
		scenario.run(t, false, func(ctx command.Context, p *playerAssembly) {
			rows, err := p.scope.ListEntries(ctx.State, "mail", "issued")
			if err != nil {
				t.Fatal(err)
			}
			if raw := rows["starter:prestige-avatar:65193:v1"]; raw != nil {
				if err := json.Unmarshal(raw, &linkedMail); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	readLinked()
	if linkedMail != 0 {
		t.Fatal("unowned prestige skin received an attachment gift")
	}
	scenario.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.eventEconomy.Apply(ctx, "new-owned-prestige", nil, []gamedata.Reward{{Type: 45, ID: 65193, Count: 1}}); err != nil {
			t.Fatal(err)
		}
		// Another legitimate mail rewards one shared piece before the linked gift.
		if err := p.mailService.EnqueueCompensation(ctx, "shared-piece-source", "Shared piece", "Another source of the same piece", []gamedata.Reward{{Type: 49, ID: 10025, Count: 1}}, time.Now()); err != nil {
			t.Fatal(err)
		}
	})
	g.call("/MailInfo", nil)
	readLinked()
	if linkedMail == 0 {
		t.Fatal("new ownership did not produce its linked outfit in the current session")
	}
	var sharedMail uint64
	scenario.run(t, false, func(ctx command.Context, p *playerAssembly) {
		rows, err := p.scope.ListEntries(ctx.State, "mail", "issued")
		if err != nil || json.Unmarshal(rows["shared-piece-source"], &sharedMail) != nil {
			t.Fatal("shared-piece source mail missing", err)
		}
	})
	g.call("/MailOpen", appScalar(2, sharedMail))
	claim := g.call("/MailOpen", appScalar(2, linkedMail))
	var newlyAcquired []uint64
	for _, bundle := range appRows(claim, 1) {
		for _, row := range appRows(bundle, 1) {
			if appValue(row, 3) == 49 {
				newlyAcquired = append(newlyAcquired, appValue(row, 2))
			}
		}
	}
	slices.Sort(newlyAcquired)
	if !slices.Equal(newlyAcquired, []uint64{20033, 40048}) {
		t.Fatalf("linked gift returned unexpected new pieces: %v", newlyAcquired)
	}
	for _, id := range []uint64{10025, 20033, 40048} {
		if g.itemCount(49, id) != 1 {
			t.Fatalf("linked piece %d duplicated or missing", id)
		}
	}
	if g.itemCount(61, 15) != 0 || g.itemCount(49, 10023) != 0 || g.itemCount(49, 50013) != 0 {
		t.Fatal("linked prestige gift granted a default avatar, default outfit, or title reward")
	}
}
