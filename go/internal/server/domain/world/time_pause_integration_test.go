//go:build integration

package world

import (
	"testing"
	"time"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func timePauseRequest(seq, action uint64) []byte {
	request := wire.AppendVarint(nil, 1, seq)
	if action != 0 {
		request = wire.AppendVarint(request, 2, action)
	}
	return request
}

func pauseCall(t *testing.T, service *Service, ctx command.Context, seq, action uint64) int64 {
	t.Helper()
	code, body, handled, err := service.Handle(ctx, "/TimePause", timePauseRequest(seq, action))
	if err != nil || !handled || code != 100 {
		t.Fatalf("TimePause action %d: handled=%t code=%d error=%v", action, handled, code, err)
	}
	seconds, _, err := wire.Varint(body, 1)
	if err != nil {
		t.Fatal(err)
	}
	return int64(seconds)
}

func TestFieldMenuPausePreservesLimitedMonsterLifetimeAndRespawn(t *testing.T) {
	ctx, service, _, _ := eventMonsterIntegration(t, 7)
	now := service.monsterTime()
	service.monsterNow = func() time.Time { return now }
	designs, err := service.monsterLoader(7)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := service.loadMonsterState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	shortestLife := time.Duration(1<<63 - 1)
	for _, design := range designs {
		if design.LifeSeconds == 0 {
			continue
		}
		if _, err := service.monsterState(&monsters, 7, design); err != nil {
			t.Fatal(err)
		}
		count++
		shortestLife = min(shortestLife, time.Duration(design.LifeSeconds)*time.Second)
	}
	if count == 0 {
		t.Fatal("selected GameData must supply limited-lifetime field monsters")
	}
	if err := service.saveMonsterState(ctx, monsters); err != nil {
		t.Fatal(err)
	}
	now = now.Add(shortestLife / 2)
	pauseCall(t, service, ctx, 1, 0)
	pausedAt := now
	now = now.Add(30 * time.Second)
	during, err := service.loadMonsterState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, design := range designs {
		if design.LifeSeconds == 0 {
			continue
		}
		original := monsters.Monsters[monsterKey(7, design.ID)]
		current, err := service.monsterState(&during, 7, design)
		if err != nil || current.Generation != original.Generation || current.Defeated || current.LifeEnd-now.UnixMilli() != original.LifeEnd-pausedAt.UnixMilli() {
			t.Fatalf("menu time expired or regenerated monster %d: %v", design.ID, err)
		}
	}
	if seconds := pauseCall(t, service, ctx, 2, 1); seconds != 30 {
		t.Fatalf("client must receive the actual 30 paused seconds, got %d", seconds)
	}
	for _, design := range designs {
		if design.LifeSeconds == 0 {
			continue
		}
		resumed, err := service.loadMonsterState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		state := resumed.Monsters[monsterKey(7, design.ID)]
		now = time.UnixMilli(state.LifeEnd)
		state, err = service.monsterState(&resumed, 7, design)
		if err != nil || !state.Defeated || state.Respawn != state.LifeEnd+int64(design.RegenSeconds)*1000 {
			t.Fatalf("resumed monster %d did not expire into its authored regeneration delay: %v", design.ID, err)
		}
		if err := service.saveMonsterState(ctx, resumed); err != nil {
			t.Fatal(err)
		}
		pauseCall(t, service, ctx, 3, 0)
		now = now.Add(time.Duration(design.RegenSeconds+5) * time.Second)
		waiting, err := service.loadMonsterState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		current, err := service.monsterState(&waiting, 7, design)
		if err != nil || !current.Defeated || current.Generation != state.Generation || current.Respawn-now.UnixMilli() != int64(design.RegenSeconds)*1000 {
			t.Fatalf("paused monster %d regenerated before resume: %v", design.ID, err)
		}
		pauseCall(t, service, ctx, 4, 1)
		now = time.UnixMilli(current.Respawn)
		regenerated, err := service.monsterState(&waiting, 7, design)
		if err != nil || regenerated.Defeated || regenerated.Generation != state.Generation+1 {
			t.Fatalf("resumed monster %d failed to regenerate at its new deadline: %v", design.ID, err)
		}
		break
	}
}

func TestFieldPauseDoesNotDelayWeeklyResetOrLeakAcrossSessions(t *testing.T) {
	ctx, service, _, _ := eventMonsterIntegration(t, 2)
	now := service.monsterTime()
	service.monsterNow = func() time.Time { return now }
	designs, err := service.monsterLoader(2)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := service.loadMonsterState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, design := range designs {
		if design.ResetType != 2 || design.LifeSeconds != 0 {
			continue
		}
		state, err := service.monsterState(&monsters, 2, design)
		if err != nil {
			t.Fatal(err)
		}
		state.Defeated = true
		state.Respawn = service.nextMonsterSpawn(design)
		monsters.Monsters[monsterKey(2, design.ID)] = state
		if err := service.saveMonsterState(ctx, monsters); err != nil {
			t.Fatal(err)
		}
		pauseCall(t, service, ctx, 1, 0)
		now = time.UnixMilli(state.Respawn).Add(time.Second)
		pauseCall(t, service, ctx, 2, 1)
		after, err := service.loadMonsterState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		refreshed, err := service.monsterState(&after, 2, design)
		if err != nil || refreshed.Defeated || refreshed.Generation <= state.Generation {
			t.Fatalf("UI pause delayed the weekly reward reset: %v", err)
		}
		pauseCall(t, service, ctx, 3, 0)
		now = now.Add(10 * time.Second)
		ctx.SessionID = "new-game-session"
		if seconds := pauseCall(t, service, ctx, 4, 1); seconds != 0 {
			t.Fatal("new session received another session's paused time")
		}
		now = now.Add(time.Minute)
		if seconds := pauseCall(t, service, ctx, 5, 1); seconds != 0 {
			t.Fatal("abandoned pause remained active after session replacement")
		}
		return
	}
	t.Fatal("selected GameData must supply weekly regeneration monsters")
}
