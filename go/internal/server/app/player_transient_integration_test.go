//go:build integration

package app

import (
	"bd2server/internal/server/domain/battle"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/gateway/session"
	"bd2server/internal/server/runtime/player"
	"context"
	"testing"
	"time"
)

// Independently verified in selected 2.35.10 GameData using gamedata_db.py:
// pack22 FieldMonsterTable id8 has phase group1; PhaseBattleTable group1 rows
// id1/id2 use decks8/9. BattleEnter/Start/PhaseChange request fields are from
// the original generated Proto.Net classes, not the server's response writer.
func TestFailedBatchDiscardsRealBattlePhaseBeforeBattleCanRestart(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	owner, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	// Select the verified pack as the battle's immutable scenario resolver.
	// The production battle service loads phases from real GameData; only this
	// scenario selection avoids progressing a full story campaign in this test.
	owner.assembly.battleService = battle.NewService(factory.options.gameData, factory.options.gameDataVersion, func(command.Context) (int, error) { return 22, nil })
	owner.assembly.handlers = []session.Handler{owner.assembly.battleService}
	runtime, err := player.New(accounts[0], owner, player.Limits{})
	if err != nil {
		_ = owner.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	run := func(c player.Command) (player.Reply, error) {
		future, err := runtime.Submit(ctx, c)
		if err != nil {
			return player.Reply{}, err
		}
		return future.Wait(ctx)
	}
	enter := append(appScalar(1, 1), appScalar(3, 8)...)
	enter = append(enter, appScalar(4, 8)...)
	enter = append(enter, appScalar(5, 1)...)
	if _, err := run(registryCommand(accounts[0], "enter-phase-battle", "/BattleEnter", enter)); err != nil {
		t.Fatal(err)
	}
	start := append(appScalar(1, 2), appScalar(2, 8)...)
	if _, err := run(registryCommand(accounts[0], "start-phase-one", "/BattleStart", start)); err != nil {
		t.Fatal(err)
	}
	batch := registryCommand(accounts[0], "phase-then-invalid-member", "/BattlePhaseChange", appScalar(1, 3))
	batch.Requests = append(batch.Requests, player.Request{Path: "/BattleStart", Body: append(appScalar(1, 4), appScalar(2, 999999)...)})
	batch.Digest[0] ^= 1
	if _, err := run(batch); err == nil {
		t.Fatal("invalid next phase start committed its preceding phase transition")
	}
	identity := command.Context{Identity: command.Identity{AccountID: accounts[0], SessionID: "registry-client", RequestID: "inspect-recovered-battle"}}
	if owner.assembly.battleService.Active(identity) {
		t.Fatal("recovered account retained uncommitted in-battle ownership")
	}
	if _, err := run(registryCommand(accounts[0], "invalid-before-enter", "/BattleStart", append(appScalar(1, 5), appScalar(2, 9)...))); err == nil {
		t.Fatal("post-recovery battle start bypassed required enter")
	}
}
