//go:build integration

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/progression/conditions"
	"bd2server/internal/server/domain/roster"
)

func missionGrant(t *testing.T, g *missionGameplay, rewards ...gamedata.Reward) {
	t.Helper()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx := command.Context{State: tx, InstanceIDs: g.factory.instanceIDs()}
	if _, err := g.owner.assembly.eventEconomy.Apply(ctx, "mission-condition:"+t.Name(), nil, rewards); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// persistedEventMissionValue reads the saved event mission without asking the
// service, whose own reconciliation would hide whether login did the work.
func persistedEventMissionValue(t *testing.T, g *missionGameplay, taskID uint64) uint64 {
	t.Helper()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	raw, err := g.owner.assembly.gameplayStore.Load(tx, "eventtasks")
	if err != nil {
		t.Fatal(err)
	}
	if raw == nil {
		return 0
	}
	var state struct {
		Missions map[string]struct{ Value uint64 }
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	suffix := "/" + strconv.FormatUint(taskID, 10)
	var matches []uint64
	for key, mission := range state.Missions {
		if strings.HasSuffix(key, suffix) {
			matches = append(matches, mission.Value)
		}
	}
	if len(matches) > 1 {
		t.Fatalf("mission %d appears under %d schedules", taskID, len(matches))
	}
	if len(matches) == 0 {
		return 0
	}
	return matches[0]
}

// persistedRegularMissionValue reads a zero-valued regular mission, which
// MissionInfo omits until its progress is non-zero.
func persistedRegularMissionValue(t *testing.T, g *missionGameplay, groupType, group, id uint64) uint64 {
	t.Helper()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	raw, err := g.owner.assembly.scope.Load(tx, "missions")
	if err != nil {
		t.Fatal(err)
	}
	if raw == nil {
		return 0
	}
	var state struct{ Progress map[string]uint64 }
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return state.Progress[fmt.Sprintf("%d/%d/%d", groupType, group, id)]
}

// guideTask finds a step-one guide mission without querying EventMissionInfo,
// which would reconcile the very progress under test.
func guideTask(t *testing.T, g *missionGameplay, groups map[uint64]bool, condition, subtype uint64) uint64 {
	t.Helper()
	for _, task := range g.factory.design.eventTasksDesign.Missions {
		if groups[task.Group] && task.Type == condition && task.SubType == subtype {
			return task.ID
		}
	}
	t.Fatalf("no guide step mission with condition %d subtype %d", condition, subtype)
	return 0
}

// setPermanentCharacterLevel rewrites one saved permanent character, emulating
// progress that predates the guide step.
func setPermanentCharacterLevel(t *testing.T, g *missionGameplay, level uint64) {
	t.Helper()
	s := &trapPlayerScenario{factory: g.factory, owner: g.owner, account: g.account}
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		var target roster.Character
		for _, character := range p.worldService.CharacterService().RawAll() {
			if !roster.IsStoryCharacter(character) && !roster.IsCharmCharacter(character) {
				target = character
				break
			}
		}
		if target.InvenIndex == 0 {
			t.Fatal("fixture has no permanent character")
		}
		key := strconv.FormatUint(target.InvenIndex, 10)
		raw, found, err := p.scope.LoadEntry(ctx.State, "characters", "characters", key)
		if err != nil || !found {
			t.Fatalf("character entry %s: found=%v err=%v", key, found, err)
		}
		if err := json.Unmarshal(raw, &target); err != nil {
			t.Fatal(err)
		}
		target.Level = level
		payload, err := json.Marshal(target)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.scope.PutEntry(ctx.State, "characters", "characters", key, payload); err != nil {
			t.Fatal(err)
		}
	})
	g.reopen()
}

// MissionTable 303/3035 is guide step McCharTargetLevelUp: "reach character
// level 20 or above". The level boundary must include 20, and the saved value
// stays zero until login reconciles it.
func TestGuidePassCharacterLevelMissionBackfillsAtLogin(t *testing.T) {
	for _, test := range []struct{ level, complete uint64 }{{1, 0}, {19, 0}, {20, 1}, {21, 1}} {
		t.Run(fmt.Sprintf("level%d", test.level), func(t *testing.T) {
			g := newMissionGameplay(t)
			groups, _ := guideStepOne(t, g)
			id := guideTask(t, g, groups, 207, 20)
			if test.level > 1 {
				setPermanentCharacterLevel(t, g, test.level)
			}
			if got := persistedEventMissionValue(t, g, id); got != 0 {
				t.Fatalf("mission advanced before login: %d", got)
			}
			g.call("/LoginUser", nil)
			want := missionTarget(t, g, id) * test.complete
			if got := persistedEventMissionValue(t, g, id); got != want {
				t.Fatalf("character level mission value=%d, want %d", got, want)
			}
		})
	}
}

// MissionTable 303/3039 is guide step McPlatformConnectOrLogin. Every session
// belongs to an authenticated account, so login completes it.
func TestGuidePassPlatformLoginMissionCompletesAtLogin(t *testing.T) {
	g := newMissionGameplay(t)
	groups, _ := guideStepOne(t, g)
	id := guideTask(t, g, groups, 231, 0)
	if got := persistedEventMissionValue(t, g, id); got != 0 {
		t.Fatalf("platform login mission started at %d, want 0", got)
	}
	g.call("/LoginUser", nil)
	if got, want := persistedEventMissionValue(t, g, id), missionTarget(t, g, id); got != want {
		t.Fatalf("platform login mission value=%d, want %d", got, want)
	}
	row, _ := guideConditionMission(t, g, groups, 231, 0)
	if got, want := appValue(row, 4), missionTarget(t, g, id); got != want {
		t.Fatalf("platform login mission served value=%d, want %d", got, want)
	}
}

// A roguelike battle reports McPackBattle with the GameData roguelike pack and
// the ROGUE_LIKE battle mode, which completes the generic daily pack-battle
// mission.
func TestRogueLikeBattleReportsPackAndMode(t *testing.T) {
	g := newMissionGameplay(t)
	// GameData mission 1/112 is inside its 1999-01-01..2024-05-13 window.
	g.now = time.Date(2024, time.May, 1, 12, 0, 0, 0, time.UTC)
	event := g.owner.assembly.rogueLikeBattleEvent()
	if event.Type != conditions.PackBattle || event.Sub != g.factory.design.roguelike.Pack || event.Param != battleModeRogueLike || event.Count != 1 {
		t.Fatalf("roguelike battle event = %+v", event)
	}
	if battleModeRogueLike != 21 {
		t.Fatalf("roguelike battle mode = %d, want ROGUE_LIKE 21", battleModeRogueLike)
	}
	g.call("/LoginUser", nil)
	if got := persistedRegularMissionValue(t, g, 0, 1, 112); got != 0 {
		t.Fatalf("pack battle mission started at %d, want 0", got)
	}
	s := &trapPlayerScenario{factory: g.factory, owner: g.owner, account: g.account}
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if err := p.recordRogueLikeBattle(ctx); err != nil {
			t.Fatal(err)
		}
	})
	g.reopen()
	if got := persistedRegularMissionValue(t, g, 0, 1, 112); got != 1 {
		t.Fatalf("pack battle mission value=%d, want 1", got)
	}
}

// MissionTable 2/208 is McTalentSkillUse with SubType 9
// (Define_TalentSkillType TS_EQUIPMENT_MAKING): one successful equipment
// making request completes it.
func TestEquipmentMakingCompletesTalentSkillMission(t *testing.T) {
	g := newMissionGameplay(t)
	g.call("/LoginUser", nil)
	var character uint64
	var materials []byte
	s := &trapPlayerScenario{factory: g.factory, owner: g.owner, account: g.account}
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.collection.GrantCostumes(ctx, "talent-skill-mission", []uint64{1401}, p.design.regularGacha); err != nil {
			t.Fatal(err)
		}
		for _, c := range p.worldService.CharacterService().EquipmentCharacters() {
			if _, ok := p.design.equipmentCraft.CharTalent[c.ID]; ok {
				character = c.InvenIndex
				break
			}
		}
		if character == 0 {
			t.Fatal("fixture needs a crafting character")
		}
		rewards := []gamedata.BattleReward{{Type: 8, ID: 204, Count: 50}, {Type: 8, ID: 201, Count: 50}}
		if _, err := p.ownedItems.GrantOnce(ctx, "talent-skill-materials", rewards); err != nil {
			t.Fatal(err)
		}
		if err := p.wallet.ExchangeOnce(ctx, "talent-skill-wallet", nil, []gamedata.Reward{{Type: 4, Count: 1000000}, {Type: 12, Count: 100}}); err != nil {
			t.Fatal(err)
		}
		for _, id := range []uint64{204, 201} {
			items, err := p.ownedItems.SelectMutable(ctx, 8, id, 3)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				materials = append(materials, appMessage(4, assets.ItemWire(item))...)
			}
		}
	})
	g.reopen()
	if got := persistedRegularMissionValue(t, g, 1, 2, 208); got != 0 {
		t.Fatalf("talent skill mission started at %d, want 0", got)
	}
	request := append(appScalar(2, character), appScalar(3, 1)...)
	request = append(request, materials...)
	request = append(request, appScalar(5, 0)...)
	request = append(request, appScalar(7, 1)...)
	request = append(request, appScalar(8, 3)...)
	g.call("/EquipFromMakingToUpgrade", request)
	if got := persistedRegularMissionValue(t, g, 1, 2, 208); got != 1 {
		t.Fatalf("talent skill mission value=%d, want 1", got)
	}
}

// MissionTable 1/110 is McSamGroupIdMissionUpdateCompleteCount (SubType 0):
// completing any other daily mission in group 1 completes it, while a weekly
// completion must not count.
func TestSameGroupDailyMissionCountsCompletedDailyMissions(t *testing.T) {
	g := newMissionGameplay(t)
	g.call("/LoginUser", nil)
	g.assertMission(0, 1, 101, 1, false)
	g.assertMission(0, 1, 110, 1, false)
	g.assertMission(1, 2, 210, 2, false)
}

// MissionTable 1/111 and 302/3029 are McgachaBuy: one regular draw completes
// both the daily mission and the guide step, and further draws do not exceed
// their targets.
func TestGachaMissionsProgressFromOneDraw(t *testing.T) {
	g := newMissionGameplay(t)
	groups, _ := guideStepOne(t, g)
	row, guideID := guideConditionMission(t, g, groups, 9, 0)
	if row == nil {
		t.Fatal("guide gacha mission missing from EventMissionInfo")
	}
	if got := appValue(row, 4); got != 0 {
		t.Fatalf("guide gacha mission started at %d, want 0", got)
	}
	g.call("/LoginUser", nil)
	if got := persistedRegularMissionValue(t, g, 0, 1, 111); got != 0 {
		t.Fatalf("daily gacha mission started at %d, want 0", got)
	}
	// Pick a plain single draw paid with free jewelry, not a step-up, limited,
	// selection or first-gacha group, so two identical draws stay valid.
	catalog := g.factory.design.regularGacha
	var groupID, gachaID, price uint64
	for _, group := range catalog.Groups() {
		design, found := catalog.Gacha(group.OneTimeGachaID)
		if !found || design.Count != 1 || design.PriceType != 3 || design.Price == 0 {
			continue
		}
		if group.BuyLimitCount != 0 || group.SelectCount != 0 || group.GachaSubType != 0 {
			continue
		}
		if _, stepUp := catalog.StepForGacha(group.OneTimeGachaID); stepUp {
			continue
		}
		if g.factory.design.firstGacha != nil && g.factory.design.firstGacha.GachaID == group.OneTimeGachaID {
			continue
		}
		groupID, gachaID, price = group.ID, group.OneTimeGachaID, design.Price
		break
	}
	if groupID == 0 {
		t.Fatal("GameData has no single-draw gacha paid with free jewelry")
	}
	missionGrant(t, g, gamedata.Reward{Type: 3, ID: 0, Count: price * 2})
	g.call("/GachaBuy", append(appScalar(2, gachaID), appScalar(3, 1)...))
	g.assertMission(0, 1, 111, 1, false)
	row, _ = guideConditionMission(t, g, groups, 9, 0)
	if got, want := appValue(row, 4), missionTarget(t, g, guideID); got != want {
		t.Fatalf("guide gacha mission value=%d, want %d", got, want)
	}
	g.call("/GachaBuy", append(appScalar(2, gachaID), appScalar(3, 1)...))
	g.assertMission(0, 1, 111, 1, false)
	row, _ = guideConditionMission(t, g, groups, 9, 0)
	if got, want := appValue(row, 4), missionTarget(t, g, guideID); got != want {
		t.Fatalf("second draw inflated guide gacha mission to %d, want %d", got, want)
	}
}
