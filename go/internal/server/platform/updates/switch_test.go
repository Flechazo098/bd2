package updates

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"bd2server/internal/common/release"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// installed lays out an old version with its data directory inside the
// program directory, the default layout, and a staged new version.
func installed(t *testing.T) (install, staged, rollback string) {
	root := t.TempDir()
	install = filepath.Join(root, "server")
	writeFile(t, filepath.Join(install, "bd2server"), "old binary")
	writeFile(t, filepath.Join(install, "go", "seed", "old.json"), "old seed")
	writeFile(t, filepath.Join(install, "data", "state", "player.db"), "save")
	staged = filepath.Join(root, "staged")
	writeFile(t, filepath.Join(staged, "bd2server"), "new binary")
	writeFile(t, filepath.Join(staged, "go", "seed", "new.json"), "new seed")
	writeFile(t, filepath.Join(staged, "data", "state", ".keep"), "")
	return install, staged, filepath.Join(install, ".update-rollback", "2.36.7+server.0.1.0")
}

func TestSwapReplacesProgramFilesButNeverData(t *testing.T) {
	install, staged, rollback := installed(t)
	names, err := swap(install, staged, rollback)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(install, "bd2server")); got != "new binary" {
		t.Fatalf("program after swap = %q", got)
	}
	if _, err := os.Stat(filepath.Join(install, "go", "seed", "old.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("files of the old version remained mixed into the new one")
	}
	if got := readFile(t, filepath.Join(install, "data", "state", "player.db")); got != "save" {
		t.Fatalf("save after swap = %q", got)
	}

	if err := restore(install, rollback, names); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(install, "bd2server")); got != "old binary" {
		t.Fatalf("program after restore = %q", got)
	}
	if got := readFile(t, filepath.Join(install, "go", "seed", "old.json")); got != "old seed" {
		t.Fatalf("seed after restore = %q", got)
	}
	if got := readFile(t, filepath.Join(install, "data", "state", "player.db")); got != "save" {
		t.Fatalf("save after restore = %q", got)
	}
}

func switched(t *testing.T, migration string) (dataDir, install string) {
	install, staged, rollback := installed(t)
	names, err := swap(install, staged, rollback)
	if err != nil {
		t.Fatal(err)
	}
	dataDir = filepath.Join(install, "data")
	record := pending{Install: install, From: "2.36.7+server.0.1.0", To: "2.36.7+server.0.2.0", Names: names, Rollback: rollback, Migration: release.Migration{Kind: migration}}
	if err := writeJSON(filepath.Join(directory(dataDir), pendingFile), record); err != nil {
		t.Fatal(err)
	}
	return dataDir, install
}

func TestRepeatedFailedStartsRestoreThePreviousProgram(t *testing.T) {
	dataDir, install := switched(t, release.MigrationNone)
	for attempt := 1; attempt <= maxStartAttempts; attempt++ {
		confirmation, _, err := Startup(context.Background(), dataDir, "2.36.7+server.0.2.0")
		if err != nil || confirmation == nil {
			t.Fatalf("start %d: confirmation=%v err=%v; the new version deserves %d attempts", attempt, confirmation, err, maxStartAttempts)
		}
	}
	_, code, err := Startup(context.Background(), dataDir, "2.36.7+server.0.2.0")
	if err == nil || code != ExitRestart {
		t.Fatalf("start after %d failures: code=%d err=%v; want a rollback and a restart", maxStartAttempts, code, err)
	}
	if got := readFile(t, filepath.Join(install, "bd2server")); got != "old binary" {
		t.Fatalf("program after rollback = %q", got)
	}
	status, err := ReadStatus(dataDir)
	if err != nil || status.LastResult == nil || status.LastResult.Outcome != OutcomeRolledBack {
		t.Fatalf("recorded result = %+v, %v", status.LastResult, err)
	}
	if _, err := os.Stat(filepath.Join(directory(dataDir), pendingFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the rollback left the pending switch behind: %v", err)
	}
}

func TestIrreversibleMigrationFailureStopsForTheOperator(t *testing.T) {
	dataDir, install := switched(t, release.MigrationIrreversible)
	confirmation, _, err := Startup(context.Background(), dataDir, "2.36.7+server.0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	code, err := confirmation.Failed(errors.New("open stores: schema 7 unsupported"))
	if err == nil || code != ExitNeedsOperator {
		t.Fatalf("Failed = %d, %v; want ExitNeedsOperator so systemd stops restarting", code, err)
	}
	if got := readFile(t, filepath.Join(install, "bd2server")); got != "new binary" {
		t.Fatalf("an irreversible release was rolled back automatically: program = %q", got)
	}
}

func TestDockerHandoffWaitsForHostResultAndNeverConfirmsAtStartup(t *testing.T) {
	dataDir := t.TempDir()
	request := dockerRequest{From: "2.36.7+server.0.1.0", To: "2.36.7+server.0.2.0", At: time.Now().UTC()}
	if err := writeJSON(filepath.Join(directory(dataDir), "docker-request.processing.json"), request); err != nil {
		t.Fatal(err)
	}
	if hold, err := awaitHost(context.Background(), dataDir, request.To); err != nil || hold {
		t.Fatalf("the new container cannot start for the host's readiness probe: hold=%v err=%v", hold, err)
	}
	var status Status
	if err := readJSON(filepath.Join(directory(dataDir), stateFile), &status); err == nil && status.LastResult != nil {
		t.Fatal("merely starting the new container falsely confirmed an update before the host's health check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if hold, err := awaitHost(ctx, dataDir, request.From); !hold || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the old container resumed before the host reported an outcome: hold=%v err=%v", hold, err)
	}
	result := hostResult{Result: Result{From: request.From, To: request.To, Outcome: OutcomeRolledBack, At: time.Now().UTC()}, RequestAt: request.At}
	if err := writeJSON(filepath.Join(directory(dataDir), "docker-result.json"), result); err != nil {
		t.Fatal(err)
	}
	if hold, err := awaitHost(context.Background(), dataDir, request.From); err != nil || hold {
		t.Fatalf("the host could not start its fallback container: hold=%v err=%v", hold, err)
	}
	status, err := ReadStatus(dataDir)
	if err != nil || status.LastResult == nil || status.LastResult.Outcome != OutcomeRolledBack {
		t.Fatalf("the rollback result was not available to the CLI: %+v, %v", status, err)
	}
}

func TestNativeUpdateRequiresContinuousHealthBeforeItStopsBeingPending(t *testing.T) {
	dataDir, install := switched(t, release.MigrationNone)
	confirmation, _, err := Startup(context.Background(), dataDir, "2.36.7+server.0.2.0")
	if err != nil || confirmation == nil {
		t.Fatalf("start new program: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	sampled := make(chan struct{})
	done := make(chan struct{})
	var ready atomic.Bool
	go func() {
		defer close(done)
		confirmation.observeHealthy(ctx, func() bool {
			value := ready.Load()
			select {
			case sampled <- struct{}{}:
			case <-ctx.Done():
			}
			return value
		}, ticks)
	}()
	t.Cleanup(func() { cancel(); <-done })
	at := time.Now()
	tick := func(seconds int) {
		t.Helper()
		select {
		case ticks <- at.Add(time.Duration(seconds) * time.Second):
		case <-done:
			t.Fatal("the update was confirmed before 90 seconds of continuous health")
		case <-time.After(3 * time.Second):
			t.Fatal("the health monitor stopped consuming readiness samples")
		}
		select {
		case <-sampled:
		case <-time.After(3 * time.Second):
			t.Fatal("the health monitor did not sample readiness")
		}
	}
	tick(0)
	tick(90)
	ready.Store(true)
	tick(100)
	tick(145)
	ready.Store(false)
	tick(180)
	ready.Store(true)
	tick(181)
	tick(270)
	if _, err := os.Stat(filepath.Join(directory(dataDir), pendingFile)); err != nil {
		t.Fatal("an interrupted healthy interval removed the rollback protection")
	}
	tick(271)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("90 seconds of continuous readiness never confirmed the update")
	}
	status, err := ReadStatus(dataDir)
	if err != nil || status.LastResult == nil || status.LastResult.Outcome != OutcomeSucceeded {
		t.Fatalf("successful health confirmation was not recorded: %+v %v", status, err)
	}
	if _, err := os.Stat(filepath.Join(directory(dataDir), pendingFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the confirmed update remained pending: %v", err)
	}
	for range 5 {
		confirmation, code, err := Startup(context.Background(), dataDir, "2.36.7+server.0.2.0")
		if confirmation != nil || code != 0 || err != nil {
			t.Fatalf("restarting a confirmed version was treated as another failed attempt: %v %d %v", confirmation, code, err)
		}
	}
	assertion := readFile(t, filepath.Join(install, "bd2server"))
	if assertion != "new binary" {
		t.Fatalf("a confirmed version was rolled back on subsequent starts: %q", assertion)
	}
}

func TestDockerHandoffDoesNotAcceptAStaleResult(t *testing.T) {
	dataDir := t.TempDir()
	request := dockerRequest{From: "2.36.7+server.0.1.0", To: "2.36.7+server.0.2.0", At: time.Now().UTC().Add(-hostWait - time.Minute)}
	if err := writeJSON(filepath.Join(directory(dataDir), dockerFile), request); err != nil {
		t.Fatal(err)
	}
	stale := hostResult{Result: Result{From: request.From, To: request.To, Outcome: OutcomeRolledBack}, RequestAt: request.At.Add(-time.Hour)}
	if err := writeJSON(filepath.Join(directory(dataDir), "docker-result.json"), stale); err != nil {
		t.Fatal(err)
	}
	_, code, err := Startup(context.Background(), dataDir, request.From)
	if err == nil || code != ExitNeedsOperator {
		t.Fatalf("an old result reopened the server during an unconfirmed update: code=%d err=%v", code, err)
	}
}
