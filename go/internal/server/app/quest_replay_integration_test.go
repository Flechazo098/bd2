//go:build integration

package app

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/runtime/player"
	"context"
	"fmt"
	"maps"
	"testing"
)

// QuestReplayPopupUI accepts PackTable main quest 1 with is_retry once hard (2)
// is complete; QuestPopupUI hides rewards while QuestPacket replaces the active
// quest with QuestRetryDBInfo from accept/clear/give-up and PackInfo field 7.
func TestMainQuestReplayTracksProgressWithoutRewardsAndRestoresSelection(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	mainQuests := factory.design.world.Story.Packs[1].MainQuestIDs
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
	for difficulty := 0; difficulty <= 2; difficulty++ {
		for _, id := range mainQuests {
			if err := state.ClearQuest(ctx, id, 1, difficulty); err != nil {
				t.Fatal(err)
			}
		}
	}
	completed := progress.QuestSelection{Difficulty: 2}
	if err := state.SelectQuest(ctx, 1, completed); err != nil {
		t.Fatal(err)
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
	seq := uint64(0)
	request := func(path string, fields ...uint64) player.Command {
		seq++
		body := appScalar(1, seq)
		for i := 0; i+1 < len(fields); i += 2 {
			body = append(body, appScalar(int(fields[i]), fields[i+1])...)
		}
		c := registryCommand(accounts[0], fmt.Sprintf("replay-%d%s", seq, path), path, body)
		c.Identity.SessionID = "replay"
		return c
	}
	run := func(path string, fields ...uint64) []byte {
		t.Helper()
		return registryExecute(t, runtime, request(path, fields...)).Responses[0].Body
	}
	rejected := func(path string, fields ...uint64) {
		t.Helper()
		future, err := runtime.Submit(context.Background(), request(path, fields...))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := future.Wait(context.Background()); err == nil {
			t.Fatalf("%s %v was accepted", path, fields)
		}
	}
	packReplay := func() (retrying bool, replays [][]byte) {
		t.Helper()
		info := run("/PackInfo")
		for _, row := range appRows(info, 1) {
			if appValue(row, 1) == 1 {
				retrying = appValue(row, 9) == 1
			}
		}
		return retrying, appRows(info, 7)
	}
	if retrying, replays := packReplay(); retrying || len(replays) != 0 {
		t.Fatal("a pack without a replay reported one")
	}
	first, second := uint64(mainQuests[0]), uint64(mainQuests[1])
	rejected("/QuestAccept", 2, second, 3, 1, 4, 1, 6, 1)
	accepted := appRows(run("/QuestAccept", 2, first, 3, 1, 4, 1, 6, 1, 7, 1), 5)
	if len(accepted) != 1 || appValue(accepted[0], 1) != 1 || appValue(accepted[0], 2) != 1 || appValue(accepted[0], 3) != first || appValue(accepted[0], 6) != 1 {
		t.Fatalf("accept did not start a hard replay with battle skip: %x", accepted)
	}
	run("/QuestUpdate", 2, first, 3, 1, 4, 3)
	run("/QuestRetryBattleSkipSave", 2, 1, 3, 0)
	retrying, replays := packReplay()
	if !retrying || len(replays) != 1 || appValue(replays[0], 3) != first || appValue(replays[0], 5) != 3 || appValue(replays[0], 6) != 0 {
		t.Fatalf("relogin lost replay progress or battle skip toggle: retry=%v %x", retrying, replays)
	}
	continued := appRows(run("/QuestAccept", 2, first, 3, 1, 4, 1, 6, 1), 5)
	if len(continued) != 1 || appValue(continued[0], 5) != 3 {
		t.Fatalf("continuing the replay discarded its progress: %x", continued)
	}

	gold := owner.assembly.wallet.Snapshot(command.Context{}).Gold
	items := appItemTotals(run("/ItemInfo"))
	clear := run("/QuestClear", 2, first, 3, 1)
	next := appRows(clear, 16)
	if len(appRows(clear, 1)) != 1 || len(appRows(clear, 1)[0]) != 0 || len(next) != 1 || appValue(next[0], 3) != second || appValue(next[0], 2) != 1 {
		t.Fatalf("replay clear paid rewards or did not advance: %x", clear)
	}
	if owner.assembly.wallet.Snapshot(command.Context{}).Gold != gold || !maps.Equal(appItemTotals(run("/ItemInfo")), items) {
		t.Fatal("replay clear changed owned rewards")
	}
	rejected("/QuestClear", 2, first, 3, 1)

	ended := appRows(run("/QuestGiveUp", 2, second, 3, 1), 5)
	if len(ended) != 1 || appValue(ended[0], 1) != 1 || appValue(ended[0], 3) != 0 {
		t.Fatalf("give up did not end the client replay: %x", ended)
	}
	if retrying, replays := packReplay(); retrying || len(replays) != 0 {
		t.Fatal("abandoned replay survived")
	}
	if selection, _ := state.Selection(1); selection != completed {
		t.Fatalf("give up left replay selection %+v", selection)
	}
	rejected("/QuestRetryBattleSkipSave", 2, 1, 3, 1)

	run("/QuestAccept", 2, first, 3, 1, 6, 1)
	quest := first
	for range mainQuests {
		retry := appRows(run("/QuestClear", 2, quest, 3, 1), 16)
		if len(retry) != 1 {
			t.Fatalf("replay clear of quest %d omitted QuestRetryInfo", quest)
		}
		if quest = appValue(retry[0], 3); quest == 0 {
			break
		}
	}
	if quest != 0 {
		t.Fatal("replay did not end after the final main quest")
	}
	if retrying, replays := packReplay(); retrying || len(replays) != 0 {
		t.Fatal("finished replay survived")
	}
	if selection, _ := state.Selection(1); selection != completed {
		t.Fatalf("finished replay left selection %+v", selection)
	}
}
