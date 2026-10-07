//go:build integration

package app

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/runtime/player"
	accountstate "bd2server/internal/server/storage/account"
	identitystore "bd2server/internal/server/storage/identity"
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type gatedPlayerExecutor struct {
	owner   *playerInstance
	entered chan struct{}
	release chan struct{}
	closed  chan struct{}
	once    sync.Once
}

type gatedIdentityProfile struct {
	source interface {
		GameIdentity(context.Context, string) (identitystore.GameProfile, error)
	}
	accountID string
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (g *gatedIdentityProfile) GameIdentity(ctx context.Context, accountID string) (identitystore.GameProfile, error) {
	if accountID == g.accountID {
		g.once.Do(func() { close(g.entered); <-g.release })
	}
	return g.source.GameIdentity(ctx, accountID)
}

func (g *gatedPlayerExecutor) Execute(ctx context.Context, c player.Command) (player.Reply, error) {
	g.once.Do(func() { close(g.entered); <-g.release })
	return g.owner.Execute(ctx, c)
}
func (g *gatedPlayerExecutor) Recover(ctx context.Context) error { return g.owner.Recover(ctx) }
func (g *gatedPlayerExecutor) Close() error                      { err := g.owner.Close(); close(g.closed); return err }

func registryCommand(accountID, identity, path string, body []byte) player.Command {
	return player.Command{Identity: command.Identity{AccountID: accountID, SessionID: "registry-client", RequestID: identity}, Digest: sha256.Sum256(append([]byte(path), body...)), Requests: []player.Request{{Path: path, Body: body}}}
}

func registryExecute(t *testing.T, runtime *player.Runtime, c player.Command) player.Reply {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	future, err := runtime.Submit(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := future.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return reply
}

func TestRegistryKeepsOnePlayerOwnerUntilAcceptedProgressDrains(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	owner, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	gate := &gatedPlayerExecutor{owner: owner, entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	old, err := player.New(accounts[0], gate, player.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	registry := newPlayerRegistry(factory, time.Second)
	t.Cleanup(func() {
		if err := registry.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	defer func() {
		select {
		case <-gate.release:
		default:
			close(gate.release)
		}
	}()
	ready := make(chan struct{})
	close(ready)
	registry.mu.Lock()
	registry.players[accounts[0]] = &playerSlot{ready: ready, closed: make(chan struct{}), runtime: old, lastUsed: time.Now()}
	registry.mu.Unlock()
	leased, release, err := registry.Acquire(context.Background(), accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	registry.expire(time.Now().Add(time.Hour))
	registry.mu.Lock()
	held := registry.players[accounts[0]]
	registry.mu.Unlock()
	if held == nil || held.retiring {
		t.Fatal("idle unload closed a leased player")
	}
	position := []byte(`{"MapId":1,"PlayerPosition":{"x":61,"y":2,"z":3},"ColleaguePositions":[]}`)
	body := append(appScalar(1, 7), appScalar(2, uint64(factory.options.gameRules.Story.StartPackID))...)
	body = append(body, appMessage(3, position)...)
	ctx, cancelWait := context.WithCancel(context.Background())
	future, err := leased.Submit(ctx, registryCommand(accounts[0], "save-position-before-idle", "/SaveUserPosition", body))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-gate.entered:
	case <-time.After(20 * time.Second):
		t.Fatal("accepted progress did not enter owner")
	}
	cancelWait()
	release()
	release()
	expired := make(chan struct{})
	go func() { registry.expire(time.Now().Add(time.Hour)); close(expired) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		registry.mu.Lock()
		retiring := registry.players[accounts[0]].retiring
		registry.mu.Unlock()
		if retiring {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle player never entered retirement")
		}
		time.Sleep(time.Millisecond)
	}
	// The same account must wait for the old writer's accepted save to settle.
	short, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	if _, _, err := registry.Acquire(short, accounts[0]); !errors.Is(err, context.DeadlineExceeded) {
		stop()
		t.Fatalf("second writer opened during drain: %v", err)
	}
	stop()
	// Another account can load and process independently during A's retirement.
	type acquiredPlayer struct {
		runtime *player.Runtime
		release func()
		err     error
	}
	loaded := make(chan acquiredPlayer, 8)
	var callers sync.WaitGroup
	for range cap(loaded) {
		callers.Go(func() {
			owner, release, err := registry.Acquire(context.Background(), accounts[1])
			loaded <- acquiredPlayer{owner, release, err}
		})
	}
	callers.Wait()
	close(loaded)
	var other *player.Runtime
	var releaseOther []func()
	defer func() {
		for _, release := range releaseOther {
			release()
		}
	}()
	for acquired := range loaded {
		if acquired.err != nil {
			t.Fatal(acquired.err)
		}
		if other != nil && other != acquired.runtime {
			t.Fatal("simultaneous same-account loads created different owners")
		}
		other = acquired.runtime
		releaseOther = append(releaseOther, acquired.release)
	}
	registryExecute(t, other, registryCommand(accounts[1], "independent-equipment", "/EquipInfo", appScalar(1, 8)))
	close(gate.release)
	settlement, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	if _, err := future.Wait(settlement); err != nil {
		t.Fatal("cancelled caller lost accepted progress", err)
	}
	select {
	case <-expired:
	case <-settlement.Done():
		t.Fatal(settlement.Err())
	}
	select {
	case <-gate.closed:
	default:
		t.Fatal("retirement completed before writer close")
	}
	_, releaseReloaded, err := registry.Acquire(settlement, accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	releaseReloaded()
	if err := registry.Close(settlement); err != nil {
		t.Fatal(err)
	}
	reopened, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	if saved, exists := reopened.assembly.progressState.Position(); !exists || saved.Position.PlayerPosition.X != 61 {
		t.Fatal("idle drain discarded accepted world progress")
	}
	otherReopened, err := factory.open(accounts[1])
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := otherReopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, exists := otherReopened.assembly.progressState.Position(); exists {
		t.Fatal("world progress crossed account retirement")
	}
}

func TestRegistryShutdownClosesLoadingAndLeasedAccountWriters(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	gate := &gatedIdentityProfile{source: factory.profiles, accountID: accounts[0], entered: make(chan struct{}), release: make(chan struct{})}
	factory.profiles = gate
	registry := newPlayerRegistry(factory, time.Minute)
	t.Cleanup(func() {
		if err := registry.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	defer func() {
		select {
		case <-gate.release:
		default:
			close(gate.release)
		}
	}()
	registry.mu.Lock()
	slots := make([]*playerSlot, len(accounts))
	for i, accountID := range accounts {
		slot := &playerSlot{ready: make(chan struct{}), closed: make(chan struct{}), lastUsed: time.Now()}
		registry.players[accountID] = slot
		registry.loads <- playerLoad{accountID: accountID, slot: slot}
		slots[i] = slot
	}
	registry.mu.Unlock()
	select {
	case <-gate.entered:
	case <-time.After(20 * time.Second):
		t.Fatal("account did not begin real player loading")
	}
	// Another complete account remains leased when shutdown begins.
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	leased, releaseLease, err := registry.Acquire(ctx, accounts[1])
	if err != nil {
		t.Fatal(err)
	}
	registryExecute(t, leased, registryCommand(accounts[1], "query-before-close", "/EquipInfo", appScalar(1, 1)))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := registry.Close(cancelled); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(gate.release)
	if err := registry.Close(ctx); err != nil {
		t.Fatal(err)
	}
	releaseLease()
	for _, slot := range slots {
		select {
		case <-slot.ready:
		default:
			t.Fatal("shutdown forgot a loading account waiter")
		}
	}
	for _, accountID := range accounts {
		if _, _, err := registry.Acquire(ctx, accountID); !errors.Is(err, player.ErrClosed) {
			t.Fatalf("registry accepted after shutdown: %v", err)
		}
		path := filepath.Join(factory.options.stateDirectory, "accounts", accountDirectoryName(accountID), "state.db")
		repository, err := accountstate.Open(path)
		if err != nil {
			t.Fatal("shutdown left an account writer locked", err)
		}
		if err := repository.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
