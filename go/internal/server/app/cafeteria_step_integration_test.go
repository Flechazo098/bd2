//go:build integration

package app

import (
	"encoding/json"
	"testing"

	"bd2server/internal/server/domain/command"
)

// GameFieldManager.IEnteredCafeteriaPackField claims the introduction on every
// cafeteria entry while ContentUnlockDating (ContentOpenTable group19/detail1)
// lacks its ticket. Current GameData: CafeteriaDefaultTable rewards RandomBox
// 1400001, whose dropType1 RewardGroup holds that ticket (3007) and LobbyDeco 26.
func TestCafeteriaIntroductionDeliversTheDatingTicketOnce(t *testing.T) {
	g := newMissionGameplay(t)
	ticket := g.owner.assembly.design.contentOpeningDesign.Dating.TicketID
	if ticket == 0 {
		t.Fatal("GameData dating opening rule has no ticket")
	}
	if got := g.itemCount(19, ticket); got != 0 {
		t.Fatalf("dating ticket owned before the introduction: %d", got)
	}
	g.call("/CafeteriaIntroductionStoryReward", nil)
	if got := g.itemCount(19, ticket); got != 1 {
		t.Fatalf("introduction delivered dating ticket count %d, want 1", got)
	}
	if got := g.itemCount(25, 26); got != 1 {
		t.Fatalf("introduction delivered lobby decoration count %d, want 1", got)
	}
	g.call("/CafeteriaIntroductionStoryReward", nil)
	if got, deco := g.itemCount(19, ticket), g.itemCount(25, 26); got != 1 || deco != 1 {
		t.Fatalf("repeated introduction claim changed rewards: ticket %d decoration %d", got, deco)
	}
}

// Accounts whose introduction was recorded without the ticket regain it at
// login exactly once; the introduction box is not granted a second time.
func TestCafeteriaLoginRestoresAMissingDatingTicketOnce(t *testing.T) {
	g := newMissionGameplay(t)
	ticket := g.owner.assembly.design.contentOpeningDesign.Dating.TicketID
	g.call("/CafeteriaInfo", nil)
	s := &trapPlayerScenario{factory: g.factory, owner: g.owner, account: g.account}
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		raw, err := p.gameplayStore.Load(ctx.State, "cafeteria")
		if err != nil || raw == nil {
			t.Fatalf("cafeteria state missing: %v", err)
		}
		var state map[string]json.RawMessage
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		state["Introduction"] = json.RawMessage("true")
		if raw, err = json.Marshal(state); err != nil {
			t.Fatal(err)
		}
		if err := p.gameplayStore.Save(ctx.State, "cafeteria", raw); err != nil {
			t.Fatal(err)
		}
	})
	g.reopen()
	if got := g.itemCount(19, ticket); got != 0 {
		t.Fatalf("ticket restored before login: %d", got)
	}
	g.call("/LoginUser", nil)
	if got := g.itemCount(19, ticket); got != 1 {
		t.Fatalf("login restored dating ticket count %d, want 1", got)
	}
	if got := g.itemCount(25, 26); got != 0 {
		t.Fatalf("login restoration regranted the introduction box: decoration %d", got)
	}
	// Spend the restored ticket: the restoration identity must not grant again.
	s = &trapPlayerScenario{factory: g.factory, owner: g.owner, account: g.account}
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		items, err := p.ownedItems.SelectMutable(ctx, 19, ticket, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.ownedItems.Consume(ctx, items); err != nil {
			t.Fatal(err)
		}
	})
	g.reopen()
	g.call("/LoginUser", nil)
	g.call("/CafeteriaIntroductionStoryReward", nil)
	if got, deco := g.itemCount(19, ticket), g.itemCount(25, 26); got != 0 || deco != 0 {
		t.Fatalf("restoration repeated or regranted the box: ticket %d decoration %d", got, deco)
	}
}

// MissionTable 301/3012 is guide STEP McPackQuestIdClear for pack1 quest3
// (subType=pack, params=quest). A quest cleared without any live event, as
// before this task existed, must still let the step be claimed.
func TestStepQuestClearMissionCountsAlreadyClearedQuests(t *testing.T) {
	g := newMissionGameplay(t)
	claim := append(appScalar(3, 2), appScalar(4, 301)...)
	claim = append(claim, appScalar(5, 3012)...)
	claim = append(claim, appScalar(6, 7001)...) // guide schedule, as in guide_pass_integration_test.go
	if _, err := g.execute("/MissionClear", claim); err == nil {
		t.Fatal("step claimed before pack1 quest3 was cleared")
	}
	s := &trapPlayerScenario{factory: g.factory, owner: g.owner, account: g.account}
	// The same quest number in another pack, and another pack1 quest, are not it.
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if err := p.progressState.ClearQuest(ctx, 3, 2, 0); err != nil {
			t.Fatal(err)
		}
		if err := p.progressState.ClearQuest(ctx, 4, 1, 0); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := g.execute("/MissionClear", claim); err == nil {
		t.Fatal("step claimed from a quest other than pack1 quest3")
	}
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if err := p.progressState.ClearQuest(ctx, 3, 1, 0); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := g.execute("/MissionClear", claim); err != nil {
		t.Fatalf("step not claimable after pack1 quest3 was cleared: %v", err)
	}
	if _, err := g.execute("/MissionClear", claim); err == nil {
		t.Fatal("step claimed twice")
	}
}
