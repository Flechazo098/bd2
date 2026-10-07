//go:build integration

package world

import (
	"bytes"
	"fmt"
	"slices"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

// LocalText 991 describes weekly field rewards. Forest map rewards count
// placed regeneration monsters, independently of the already cleared story
// monsters and optional main difficulty (FieldMonsterController.Regen).
func TestForestMonsterVictorySurvivesDifficultyChangeUntilWeeklyReset(t *testing.T) {
	ctx, service, source := fieldQuestIntegration(t, 1)
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	root := versions.Resolve("data/resources/GameData")
	service.attachFieldMonsterDesign(source)
	service.attachPackDetailDesign(source)
	service.fieldReset, err = gamedata.LoadFieldResetSchedule(root, versions.GameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AttachFieldMonsterState(stateio.RootStore{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 7, 0, 0, 0, 0, time.UTC)
	service.monsterNow = func() time.Time { return now }
	if err := service.state.ClearQuest(ctx, 46, 1, 0); err != nil {
		t.Fatal(err)
	}
	monsters, err := source.FieldMonsters(1)
	if err != nil {
		t.Fatal(err)
	}
	var forest gamedata.FieldMonsterDesign
	for _, m := range monsters {
		if m.ID == 101 {
			forest = m
		}
	}
	placements, err := source.FieldMonsterMaps(1)
	if err != nil {
		t.Fatal(err)
	}
	position := wire.AppendBytes(wire.AppendVarint(nil, 2, 1), 3, []byte(fmt.Sprintf(`{"MapId":%d}`, placements[forest.ID][0])))
	if err := service.state.SaveUserPosition(ctx, position); err != nil {
		t.Fatal(err)
	}
	wallet, err := assets.OpenWallet(ctx, stateio.RootStore{}, assets.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	items, err := assets.OpenInventory(ctx, stateio.RootStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stacks, err := gamedata.LoadItemStackDesign(root, versions.GameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err := items.AttachItemStacks(stacks); err != nil {
		t.Fatal(err)
	}
	graph, err := gamedata.LoadRewardGraph(root, versions.GameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	battles := battle.NewService(root, versions.GameDataVersion, service.CurrentPackID)
	battles.AttachFieldMonsters(service)
	battles.AttachRewards(func(ctx command.Context, identity string, rewards []gamedata.Reward) ([]byte, error) {
		definitions := make([]gamedata.BattleReward, len(rewards))
		for i, reward := range rewards {
			definitions[i] = gamedata.BattleReward(reward)
		}
		resolved, err := graph.Resolve(definitions)
		if err != nil {
			return nil, err
		}
		var walletRewards []gamedata.Reward
		var inventoryRewards []gamedata.BattleReward
		for _, reward := range resolved {
			if reward.Type == 4 {
				walletRewards = append(walletRewards, gamedata.Reward(reward))
			} else {
				inventoryRewards = append(inventoryRewards, reward)
			}
		}
		if _, err := wallet.GrantQuestOnce(ctx, identity, walletRewards); err != nil {
			return nil, err
		}
		_, err = items.GrantOnce(ctx, identity, inventoryRewards)
		return nil, err
	})
	handle := func(path string, request []byte) []byte {
		t.Helper()
		_, response, handled, err := battles.Handle(ctx, path, request)
		if err != nil || !handled {
			t.Fatalf("%s: handled=%v err=%v", path, handled, err)
		}
		return response
	}
	start := func(seq uint64) {
		t.Helper()
		request := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, seq), 3, uint64(forest.ID)), 4, forest.BattleDeck), 5, 2)
		request = wire.AppendVarint(request, 2, uint64(forest.GroupID))
		handle("/BattleEnter", request)
		handle("/BattleStart", wire.AppendVarint(wire.AppendVarint(nil, 1, seq+1), 2, forest.BattleDeck))
	}
	assertCount := func(want uint64) {
		t.Helper()
		count, err := service.packMonsterDefeatedCount(ctx, 1)
		if err != nil || count != want {
			t.Fatalf("forest defeat count=%d want=%d err=%v", count, want, err)
		}
	}
	assertCount(0)
	start(1)
	handle("/BattleEnd", wire.AppendVarint(wire.AppendVarint(nil, 1, 3), 2, 2))
	assertCount(0)
	if wallet.Snapshot(ctx).Gold != 0 || len(items.All(ctx)) != 0 {
		t.Fatal("a lost encounter granted victory rewards")
	}
	start(4)
	if err := service.state.SelectQuest(ctx, 1, progress.QuestSelection{Difficulty: 0}); err != nil {
		t.Fatal(err)
	}
	end := wire.AppendVarint(wire.AppendVarint(nil, 1, 6), 2, 1)
	response := handle("/BattleEnd", end)
	updates, err := nestedMessages(response, 4)
	if err != nil || len(updates) != 1 {
		t.Fatalf("victory must supply the defeated monster to the client cache: rows=%d err=%v", len(updates), err)
	}
	assertCount(1)
	gold, awarded := wallet.Snapshot(ctx).Gold, items.All(ctx)
	if gold == 0 || len(awarded) == 0 {
		t.Fatal("authored forest victory rewards were not granted")
	}
	if retry := handle("/BattleEnd", end); !bytes.Equal(response, retry) || wallet.Snapshot(ctx).Gold != gold || !slices.Equal(awarded, items.All(ctx)) {
		t.Fatal("settlement retry changed the reply or granted a second reward")
	}
	if err := service.state.SelectQuest(ctx, 1, progress.QuestSelection{Difficulty: 2}); err != nil {
		t.Fatal(err)
	}
	assertCount(1)
	service.state, err = progress.OpenStore(ctx, stateio.RootStore{})
	if err != nil {
		t.Fatal(err)
	}
	assertCount(1)
	packInfo, err := service.packInfoFor(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	packMonsters, err := nestedMessages(packInfo, 6)
	if err != nil {
		t.Fatal(err)
	}
	var restored []byte
	for _, row := range packMonsters {
		id, _, err := wire.Varint(row, 1)
		if err != nil {
			t.Fatal(err)
		}
		if id == uint64(forest.ID) {
			restored = row
		}
	}
	if !bytes.Equal(restored, updates[0]) {
		t.Fatal("pack re-entry resurrected or lost the defeated forest encounter")
	}
	reset := time.Date(2026, time.October, 12, 0, 0, 0, 0, time.UTC)
	now = reset.Add(-time.Millisecond)
	assertCount(1)
	now = reset
	assertCount(0)
	if wallet.Snapshot(ctx).Gold != gold {
		t.Fatal("weekly reset granted another reward before another victory")
	}
}
