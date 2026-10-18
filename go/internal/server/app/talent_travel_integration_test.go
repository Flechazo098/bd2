//go:build integration

package app

import (
	"bytes"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
)

// WayPointMakePopupUI sends the destination map through TalentSkillUse, then
// IUseTalentSkillWaypointMake sends WaypointUse from the map-zero temporary
// point. Pack2's safe point is1/map21 and its temporary point is7.
func TestTalentPortalPaysOnceAndAuthorizesOnlyItsSelectedActivatedDestination(t *testing.T) {
	s := newTrapPlayerScenario(t, 2)
	s.position(t, 2, 22)
	var caster, catalyst uint64
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.collection.GrantCostumes(ctx, "portal-owned-character", []uint64{701}, p.design.rewardCostumes); err != nil {
			t.Fatal(err)
		}
		for _, c := range p.collection.Characters() {
			if p.design.talentUseDesign.Characters[c.ID].Group == 1505 {
				c.TalentLevel = 1
				c.TalentExp = 0
				if err := p.collection.UpdateCharacter(ctx, c.ID, c); err != nil {
					t.Fatal(err)
				}
				caster = c.InvenIndex
			}
		}
		if caster == 0 {
			t.Fatal("portal caster not owned")
		}
		if err := p.wallet.ExchangeOnce(ctx, "portal-catalyst", nil, []gamedata.Reward{{Type: 12, Count: 100}}); err != nil {
			t.Fatal(err)
		}
		catalyst = p.wallet.Snapshot(ctx).Catalyst
	})
	cast := func(seq, mapID uint64) []byte {
		return append(append(appScalar(1, seq), appScalar(2, caster)...), appScalar(3, mapID)...)
	}
	travel := func(seq, destination uint64) []byte {
		return append(append(append(append(appScalar(1, seq), appScalar(2, 2)...), appScalar(3, 7)...), appScalar(4, destination)...), appScalar(5, 1)...)
	}
	assertBalance := func(want uint64) {
		t.Helper()
		s.run(t, false, func(ctx command.Context, p *playerAssembly) {
			if got := p.wallet.Snapshot(ctx).Catalyst; got != want {
				t.Fatalf("portal catalyst balance got%d want%d", got, want)
			}
		})
	}
	s.protocol(t, "/WaypointSave", append(append(appScalar(1, 10), appScalar(2, 2)...), appScalar(3, 1)...))
	s.protocol(t, "/WaypointSave", append(append(appScalar(1, 11), appScalar(2, 2)...), appScalar(3, 2)...))
	if _, err := s.execute("/WaypointUse", travel(12, 1)); err == nil {
		t.Fatal("temporary portal worked without a talent cast")
	}
	if _, err := s.execute("/TalentSkillUse", cast(13, 22)); err == nil {
		t.Fatal("novice portal accepted a battle map instead of a safe destination")
	}
	if _, err := s.execute("/TalentSkillUse", cast(14, 141)); err == nil {
		t.Fatal("portal accepted a map belonging to another pack")
	}
	assertBalance(catalyst)
	response := s.protocol(t, "/TalentSkillUse", cast(15, 21))
	assertBalance(catalyst - 20)
	s.reopen(t)
	if replay := s.protocol(t, "/TalentSkillUse", cast(15, 21)); !bytes.Equal(response, replay) {
		t.Fatal("reopened player lost its acknowledged cast response")
	}
	assertBalance(catalyst - 20)
	if _, err := s.execute("/WaypointUse", travel(16, 2)); err == nil {
		t.Fatal("portal authorization allowed a different activated destination")
	}
	s.protocol(t, "/WaypointUse", travel(17, 1))
	s.reopen(t)
	s.protocol(t, "/WaypointUse", travel(17, 1))
	if _, err := s.execute("/WaypointUse", travel(17, 2)); err == nil {
		t.Fatal("travel retry changed the authorized destination")
	}
	if _, err := s.execute("/WaypointUse", travel(18, 1)); err == nil {
		t.Fatal("one portal cast authorized a second journey")
	}
	assertBalance(catalyst - 20)
	s.position(t, 2, 21)
	s.reopen(t)
	s.run(t, false, func(ctx command.Context, p *playerAssembly) {
		pack, mapID, _, err := p.worldService.TalentFieldContext(ctx)
		if err != nil || pack != 2 || mapID != 21 {
			t.Fatalf("native post-warp position not restored: pack%d map%d err%v", pack, mapID, err)
		}
		c, found := p.worldService.CharacterService().Find(ctx, caster)
		if !found || c.TalentExp != 1 {
			t.Fatal("portal replay duplicated or lost earned talent experience")
		}
	})
}

// RecvTalentSkillUpgradeResponse resets Overwhelm's shared UseCount. Current
// group305 needs10 EXP and one type8/id3 book plus1000 gold for its first upgrade.
func TestOverwhelmUpgradeRestoresSpentUsesAfterReopening(t *testing.T) {
	s := newTrapPlayerScenario(t, 2)
	s.position(t, 2, 22)
	var caster uint64
	var book []byte
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.collection.GrantCostumes(ctx, "overwhelm-owned-character", []uint64{1101}, p.design.rewardCostumes); err != nil {
			t.Fatal(err)
		}
		for _, c := range p.collection.Characters() {
			if p.design.talentUseDesign.Characters[c.ID].Group == 305 {
				c.TalentLevel, c.TalentExp = 1, 9
				if err := p.collection.UpdateCharacter(ctx, c.ID, c); err != nil {
					t.Fatal(err)
				}
				caster = c.InvenIndex
			}
		}
		if caster == 0 {
			t.Fatal("overwhelm caster not owned")
		}
		if err := p.wallet.ExchangeOnce(ctx, "overwhelm-resources", nil, []gamedata.Reward{{Type: 12, Count: 200}, {Type: 4, Count: 2000}}); err != nil {
			t.Fatal(err)
		}
		items, err := p.ownedItems.GrantOnce(ctx, "overwhelm-upgrade-book", []gamedata.BattleReward{{Type: 8, ID: 3, Count: 1}})
		if err != nil || len(items) != 1 {
			t.Fatalf("upgrade book fixture: %v", err)
		}
		book = inventoryItem(items[0].InvenIndex, 3, 8, 1)
	})
	cast := append(appScalar(1, 20), appScalar(2, caster)...)
	result := s.protocol(t, "/TalentSkillUse", cast)
	if rows := appRows(result, 1); len(rows) != 1 || appValue(rows[0], 4) != 1 {
		t.Fatal("cast did not consume an overwhelm use")
	}
	upgrade := append(append(append(appScalar(1, 21), appScalar(2, caster)...), appMessage(3, book)...), appMessage(3, inventoryItem(0, 0, 4, 1000))...)
	s.protocol(t, "/TalentSkillUpgrade", upgrade)
	s.reopen(t)
	s.run(t, false, func(ctx command.Context, p *playerAssembly) {
		var c roster.Character
		c, _ = p.worldService.CharacterService().Find(ctx, caster)
		if c.TalentLevel != 2 || c.TalentExp != 10 {
			t.Fatalf("earned first upgrade not persisted: level%d EXP%d", c.TalentLevel, c.TalentExp)
		}
		info, err := p.talentUseService.PackInfo(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range appRows(info, 13) {
			if appValue(row, 1) == 305 {
				if appValue(row, 4) != 0 {
					t.Fatal("upgraded overwhelm retained its spent uses")
				}
				return
			}
		}
		t.Fatal("upgraded overwhelm usage absent")
	})
	result = s.protocol(t, "/TalentSkillUse", append(appScalar(1, 22), appScalar(2, caster)...))
	if rows := appRows(result, 1); len(rows) != 1 || appValue(rows[0], 4) != 1 {
		t.Fatal("first cast after upgrading did not start from restored uses")
	}
}
