//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"bd2server/internal/server/protocol/wire"
)

func TestFieldPauseRetriesReuseResumeReplyAndReloginClearsOwnership(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	owner := openNicknamePlayer(t, factory, accounts[0])
	t.Cleanup(func() { closeNicknamePlayer(t, owner) })
	pause := registryCommand(accounts[0], "menu-pause", "/TimePause", appScalar(1, 1))
	if _, err := owner.Execute(context.Background(), pause); err != nil {
		t.Fatal(err)
	}
	tx, err := owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	raw, _, err := tx.LoadEntry("missions", "gameplay", "field_time_pause")
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	startedMS := time.Now().Add(-30 * time.Second).UnixMilli()
	saved["started_ms"] = startedMS
	raw, err = json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.PutEntry("missions", "gameplay", "field_time_pause", raw); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	pause = registryCommand(accounts[0], "duplicate-menu-pause", "/TimePause", appScalar(1, 2))
	if _, err := owner.Execute(context.Background(), pause); err != nil {
		t.Fatal(err)
	}
	resume := registryCommand(accounts[0], "menu-resume", "/TimePause", append(appScalar(1, 3), appScalar(2, 1)...))
	resumeStarted := time.Now().UnixMilli()
	first, err := owner.Execute(context.Background(), resume)
	if err != nil {
		t.Fatal(err)
	}
	seconds, _, err := wire.Varint(first.Responses[0].Body, 1)
	if err != nil || int64(seconds) < (resumeStarted-startedMS)/1000 || int64(seconds) > (time.Now().UnixMilli()-startedMS+999)/1000 {
		t.Fatalf("repeated pause reset its start: seconds=%d error=%v", seconds, err)
	}
	retry, err := owner.Execute(context.Background(), resume)
	if err != nil || !bytes.Equal(first.Responses[0].Body, retry.Responses[0].Body) {
		t.Fatalf("resume retry lost its original response: %v", err)
	}
	repeated, err := owner.Execute(context.Background(), registryCommand(accounts[0], "menu-resume-again", "/TimePause", append(appScalar(1, 4), appScalar(2, 1)...)))
	if err != nil {
		t.Fatal(err)
	}
	seconds, _, err = wire.Varint(repeated.Responses[0].Body, 1)
	if err != nil || seconds != 0 {
		t.Fatalf("second resume applied the pause twice: seconds=%d error=%v", seconds, err)
	}
	if _, err := owner.Execute(context.Background(), registryCommand(accounts[0], "abandoned-menu", "/TimePause", appScalar(1, 5))); err != nil {
		t.Fatal(err)
	}
	login := registryCommand(accounts[0], "new-login", "/LoginUser", appScalar(1, 6))
	login.Identity.SessionID = "replacement-game-session"
	login.LoginSessionKey = []byte("0123456789abcdef0123456789abcdef")
	if _, err := owner.Execute(context.Background(), login); err != nil {
		t.Fatal(err)
	}
	loginState, err := owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err = loginState.LoadEntry("missions", "gameplay", "field_time_pause")
	if rollbackErr := loginState.Rollback(); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	clear(saved)
	if err := json.Unmarshal(raw, &saved); err != nil || saved["session"] != "" {
		t.Fatalf("successful login did not clear persisted pause ownership: %v", err)
	}
	resume = registryCommand(accounts[0], "after-login-resume", "/TimePause", append(appScalar(1, 7), appScalar(2, 1)...))
	resume.Identity.SessionID = login.Identity.SessionID
	afterLogin, err := owner.Execute(context.Background(), resume)
	if err != nil {
		t.Fatal(err)
	}
	seconds, _, err = wire.Varint(afterLogin.Responses[0].Body, 1)
	if err != nil || seconds != 0 {
		t.Fatalf("relogin left an abandoned UI pause active: seconds=%d error=%v", seconds, err)
	}
}
