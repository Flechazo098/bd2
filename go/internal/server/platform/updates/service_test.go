package updates

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bd2server/internal/common/release"
	"bd2server/internal/server/domain/notice"
)

func TestMaintenancePublishesReadableNoticesInAllGameLanguages(t *testing.T) {
	now := time.Now()
	board := notice.NewBoard(func() time.Time { return now })
	service := New(Options{Config: Default(), DataDir: t.TempDir(), ServerVersion: "2.36.7+server.0.1.0", Board: board, Now: func() time.Time { return now }})
	service.publishMaintenance("2.36.7+server.0.2.0", now.Add(time.Hour))
	for _, locale := range []string{"en_us", "ko_kr", "ja_jp", "zh_tw", "zh_cn"} {
		raw, err := langFiles.ReadFile("lang/" + locale + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var written map[string]string
		if err := json.Unmarshal(raw, &written); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"maintenance.title", "maintenance.banner", "maintenance.body", "maintenance.login"} {
			if written[key] == "" {
				t.Fatalf("%s: %s translation is missing", locale, key)
			}
		}
		entries := board.Since(0)
		if len(entries) != 2 {
			t.Fatalf("%s: maintenance lacks a banner and detail notice", locale)
		}
		for _, entry := range entries {
			content, exists := entry.Text[locale]
			if !exists || content.Title != written["maintenance.title"] {
				t.Fatalf("%s: maintenance translation is missing", locale)
			}
			if content.Title == "" || strings.Contains(content.Title, "%!") || len(content.Blocks) != 1 {
				t.Fatalf("%s: unreadable maintenance notice: %+v", locale, content)
			}
			if !strings.Contains(content.Blocks[0].Value, "2.36.7+server.0.2.0") || strings.Contains(content.Blocks[0].Value, "%!") || strings.Contains(content.Blocks[0].Value, "%[") {
				t.Errorf("%s: maintenance text did not format the actual scheduled version: %q", locale, content.Blocks[0].Value)
			}
		}
	}
	if got := board.Since(0)[0].In("en_us").Title; got != "Server maintenance" {
		t.Fatalf("maintenance title contains formatting artifacts: %q", got)
	}
}

func TestUpdateCancellationClearsMaintenanceAndAllowsAnotherSchedule(t *testing.T) {
	initial := time.Now()
	var clock atomic.Int64
	clock.Store(initial.UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	board := notice.NewBoard(now)
	config := Default()
	config.Method = MethodDocker
	service := New(Options{Config: config, DataDir: t.TempDir(), ServerVersion: "2.36.7+server.0.1.0", Board: board, Now: now})
	service.status.Available = &Available{Version: "2.36.7+server.0.2.0", Commit: strings.Repeat("a", 40), Migration: release.Migration{Kind: release.MigrationNone}}
	t.Cleanup(func() { _ = service.Cancel("test cleanup") })
	waitCountdown := func() {
		t.Helper()
		deadline := time.NewTimer(time.Second * 3)
		defer deadline.Stop()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			state := service.Status()
			if state.Maintenance != nil && state.Maintenance.Phase == PhaseCountdown && len(board.Since(0)) == 2 {
				return
			}
			select {
			case <-deadline.C:
				t.Fatalf("scheduled maintenance did not become ready: %+v", state)
			case <-ticker.C:
			}
		}
	}
	if err := service.Schedule(time.Hour, false, "owner"); err != nil {
		t.Fatal(err)
	}
	waitCountdown()
	if closed, _ := service.LoginsClosed("en_us"); closed {
		t.Fatal("new logins were closed an hour before maintenance")
	}
	clock.Store(initial.Add(time.Hour - 30*time.Second).UnixNano())
	if closed, text := service.LoginsClosed("en_us"); !closed || text == "" || strings.Contains(text, "%!") {
		t.Fatalf("the final minute did not close logins with readable text: closed=%v text=%q", closed, text)
	}
	if err := service.Cancel("owner"); err != nil {
		t.Fatal(err)
	}
	if len(board.Since(0)) != 0 || service.MaintenanceStart() != 0 {
		t.Fatal("cancellation left player notices or the countdown active")
	}
	if closed, _ := service.LoginsClosed("en_us"); closed {
		t.Fatal("cancelling maintenance did not reopen logins")
	}
	if err := service.Schedule(time.Hour, false, "owner"); err != nil {
		t.Fatalf("a cancelled maintenance blocked the next schedule: %v", err)
	}
	waitCountdown()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service.finishMaintenance(ctx, "cancelled preparation failed")
	if state := service.Status(); state.Maintenance == nil || state.Maintenance.Phase != PhaseCountdown {
		t.Fatal("a cancelled preparation cleared the later scheduled maintenance")
	}
}

func TestRestartDoesNotTrustAnUnsignedOfferSavedInStatus(t *testing.T) {
	dataDir := t.TempDir()
	previous := Status{Available: &Available{Version: "2.36.7+server.9.9.9", Image: "untrusted/image:latest", Migration: release.Migration{Kind: release.MigrationNone}}}
	if err := writeJSON(filepath.Join(directory(dataDir), stateFile), previous); err != nil {
		t.Fatal(err)
	}
	service := New(Options{Config: Default(), DataDir: dataDir, ServerVersion: "2.36.7+server.0.1.0"})
	if service.Status().Available != nil {
		t.Fatal("a status-file edit bypassed the required signed manifest check after restart")
	}
}

// Idle clients poll every three minutes, so a countdown shorter than five
// minutes is refused; five minutes is the shortest one accepted.
func TestScheduleRefusesCountdownsTooShortForIdlePlayers(t *testing.T) {
	config := Default()
	config.Method = MethodDocker
	service := New(Options{Config: config, DataDir: t.TempDir(), ServerVersion: "2.36.7+server.0.1.0", Board: notice.NewBoard(nil)})
	service.status.Available = &Available{Version: "2.36.7+server.0.2.0", Commit: strings.Repeat("a", 40), Migration: release.Migration{Kind: release.MigrationNone}}
	t.Cleanup(func() { _ = service.Cancel("test cleanup") })
	for _, delay := range []time.Duration{time.Minute, 4*time.Minute + 59*time.Second} {
		var rejection *Rejection
		if err := service.Schedule(delay, false, "owner"); !errors.As(err, &rejection) || rejection.Code != RejectInvalidDelay {
			t.Fatalf("Schedule(%v) = %v; want an invalid_delay rejection", delay, err)
		}
	}
	if err := service.Schedule(5*time.Minute, false, "owner"); err != nil {
		t.Fatalf("Schedule(5m) = %v; five minutes must be allowed", err)
	}
	// Wait until the countdown starts, which also lets maintenance finish
	// writing its state before the data directory is removed.
	deadline := time.Now().Add(3 * time.Second)
	for state := service.Status(); state.Maintenance == nil || state.Maintenance.Phase != PhaseCountdown; state = service.Status() {
		if time.Now().After(deadline) {
			t.Fatalf("a five-minute countdown did not start: %+v", state.Maintenance)
		}
		time.Sleep(time.Millisecond)
	}
}
