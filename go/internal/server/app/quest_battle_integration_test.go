//go:build integration

package app

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/runtime/player"
	"bytes"
	"context"
	"maps"
	"testing"
)

// Verified with gamedata_db.py in 20260923193640: pack1 monster2001 belongs
// to side quest103 and deck301 grants gold350 (type4, id0); monster1 belongs
// to main quest9 and decks1/200001 grant resource7 x2. These expected values
// are independent of the resolver and reward loader under test.
func TestQuestBattleUsesItsOwnQuestDifficultyAndRewardOwner(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	for _, scenario := range []struct {
		name                    string
		monster, deck, selected uint64
		difficulty, activeQuest int
		gold                    uint64
	}{
		{"normal-main", 1, 1, 1, 0, 9, 0},
		{"very-hard-main", 1, 1, 200001, 2, 9, 0},
		{"side-with-very-hard-main-selected", 2001, 301, 301, 2, 103, 350},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			owner, err := factory.open(accounts[0])
			if err != nil {
				t.Fatal(err)
			}
			tx, err := owner.repository.BeginCommand(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ctx := command.Context{Identity: command.Identity{AccountID: accounts[0], SessionID: "fixture"}, State: tx}
			state := owner.assembly.progressState
			if err := state.SetActivePackID(ctx, 1); err != nil {
				t.Fatal(err)
			}
			if err := state.SelectQuest(ctx, 1, progress.QuestSelection{Difficulty: scenario.difficulty}); err != nil {
				t.Fatal(err)
			}
			if scenario.activeQuest == 103 {
				for _, id := range []int{101, 102} {
					if err := state.ClearQuest(ctx, id, 1, 0); err != nil {
						t.Fatal(err)
					}
				}
				if err := state.AcceptQuest(ctx, 103, 1, 0); err != nil {
					t.Fatal(err)
				}
			} else {
				for id := 1; id < scenario.activeQuest; id++ {
					if err := state.ClearQuest(ctx, id, 1, scenario.difficulty); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := owner.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			runtime, err := player.New(accounts[0], owner, player.Limits{CompletedReceipts: 1})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := runtime.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			run := func(identity, path string, body []byte) []byte {
				t.Helper()
				reply := registryExecute(t, runtime, registryCommand(accounts[0], scenario.name+identity, path, body))
				return reply.Responses[0].Body
			}
			beforeGold := owner.assembly.wallet.Snapshot(command.Context{}).Gold
			beforeItems := appItemTotals(run("items-before", "/ItemInfo", appScalar(1, 1)))
			invalid := append(appScalar(1, 99), appScalar(3, scenario.monster)...)
			invalid = append(invalid, appScalar(4, 303)...)
			invalid = append(invalid, appScalar(5, 1)...)
			future, err := runtime.Submit(context.Background(), registryCommand(accounts[0], scenario.name+"wrong-monster-deck", "/BattleEnter", invalid))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := future.Wait(context.Background()); err == nil {
				t.Fatal("quest battle accepted another monster's authored deck")
			}
			if owner.assembly.battleService.Active(command.Context{Identity: command.Identity{SessionID: "registry-client"}}) {
				t.Fatal("rejected deck installed an active battle")
			}
			if owner.assembly.wallet.Snapshot(command.Context{}).Gold != beforeGold || !maps.Equal(appItemTotals(run("items-rejected", "/ItemInfo", appScalar(1, 100))), beforeItems) {
				t.Fatal("rejected deck changed owned rewards")
			}
			enter := append(appScalar(1, 2), appScalar(3, scenario.monster)...)
			enter = append(enter, appScalar(4, scenario.deck)...)
			enter = append(enter, appScalar(5, 1)...)
			response := run("enter", "/BattleEnter", enter)
			if appValue(response, 2) != scenario.selected {
				t.Fatalf("client receives wrong enemy deck: %d, want %d", appValue(response, 2), scenario.selected)
			}
			run("start", "/BattleStart", append(appScalar(1, 3), appScalar(2, scenario.selected)...))
			end := append(appScalar(1, 4), appScalar(2, 1)...)
			response = run("end", "/BattleEnd", end)
			bundles := appRows(response, 5)
			if len(bundles) != 1 {
				t.Fatal("victory omitted client reward bundle")
			}
			rewards := appItemTotals(bundles[0])
			key, count := [2]uint64{8, 7}, uint64(2)
			if scenario.gold > 0 {
				key, count = [2]uint64{4, 0}, scenario.gold
			}
			if len(rewards) != 1 || rewards[key] != count {
				t.Fatalf("wrong authored victory reward: %v, want %v x%d", rewards, key, count)
			}
			if gold := owner.assembly.wallet.Snapshot(command.Context{}).Gold; gold-beforeGold != scenario.gold {
				t.Fatalf("gold did not settle in wallet: before=%d after=%d want delta=%d", beforeGold, gold, scenario.gold)
			}
			afterItems := appItemTotals(run("items-after", "/ItemInfo", appScalar(1, 5)))
			if scenario.gold > 0 {
				if afterItems[[2]uint64{4, 0}] != 0 {
					t.Fatal("wallet currency leaked into owned inventory")
				}
			} else if afterItems[key]-beforeItems[key] != 2 {
				t.Fatal("main battle resource did not settle in owned inventory")
			}
			// ItemInfo evicts the actor's single completed-command receipt. Retrying
			// BattleEnd must still be idempotent through the battle settlement.
			retry := run("end-retry", "/BattleEnd", end)
			if !bytes.Equal(response, retry) || owner.assembly.wallet.Snapshot(command.Context{}).Gold != beforeGold+scenario.gold {
				t.Fatal("settlement retry changed its response or credited gold twice")
			}
			if totals := appItemTotals(run("items-retry", "/ItemInfo", appScalar(1, 6))); !maps.Equal(totals, afterItems) {
				t.Fatal("settlement retry credited owned items twice")
			}
		})
	}
}
