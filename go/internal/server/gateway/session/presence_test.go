package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"bd2server/internal/server/gateway/transport"
	"bd2server/internal/server/runtime/player"
)

type presenceExecutor struct{}

func (presenceExecutor) Execute(context.Context, player.Command) (player.Reply, error) {
	return player.Reply{}, nil
}
func (presenceExecutor) Recover(context.Context) error { return nil }
func (presenceExecutor) Close() error                  { return nil }

type presenceRouter struct{ runtime *player.Runtime }

func (r presenceRouter) Acquire(context.Context, string) (*player.Runtime, func(), error) {
	return r.runtime, func() {}, nil
}

func TestPresenceExpiresWithoutActivityAndOnlyValidSessionsRenewIt(t *testing.T) {
	ctx := context.Background()
	runtime, err := player.New("present-player", presenceExecutor{}, player.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(ctx) })
	server, err := NewServer(presenceRouter{runtime}, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }
	game, token, err := newGameSession("present-player", now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	game.generation = runtime.Generation()
	server.sessions[sessionTokenKey(token)] = game
	if !server.AccountOnline("present-player") || server.AccountOnline("another-player") {
		t.Fatal("presence leaked across accounts or missed authenticated activity")
	}
	now = now.Add(91 * time.Second)
	if server.AccountOnline("present-player") {
		t.Fatal("unexpired login cookie incorrectly kept disconnected player online")
	}
	invalid := Principal{AccountID: game.accountID, SessionID: game.id, Generation: game.generation + 1}
	if err := server.ValidatePrincipal(ctx, invalid); !errors.Is(err, transport.ErrGameSessionExpired) {
		t.Fatalf("stale principal accepted: %v", err)
	}
	if server.AccountOnline("present-player") {
		t.Fatal("invalid relay principal renewed presence")
	}
	valid := Principal{AccountID: game.accountID, SessionID: game.id, Generation: game.generation}
	if err := server.ValidatePrincipal(ctx, valid); err != nil {
		t.Fatal(err)
	}
	if server.AccountOnline("present-player") {
		t.Fatal("server-side periodic credential checks incorrectly renewed presence")
	}
	server.RecordActivity(invalid)
	if server.AccountOnline("present-player") {
		t.Fatal("stale relay principal recorded activity")
	}
	server.RecordActivity(valid)
	if !server.AccountOnline("present-player") {
		t.Fatal("valid native heartbeat did not renew online presence")
	}
	now = now.Add(91 * time.Second)
	if _, err := server.Principal(ctx, "s="+token); err != nil {
		t.Fatal(err)
	}
	if !server.AccountOnline("present-player") {
		t.Fatal("authenticated HTTP activity did not renew presence")
	}
	now = game.expiresAt
	if server.AccountOnline("present-player") {
		t.Fatal("expired game session retained presence")
	}
}
