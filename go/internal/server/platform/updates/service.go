package updates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bd2server/internal/common/i18n"
	"bd2server/internal/common/release"
	"bd2server/internal/server/domain/notice"
	resourcefetch "bd2server/internal/server/resources/fetch"
)

// SourceMaintenance owns the maintenance notices on the news board.
const SourceMaintenance = "maintenance"

// loginCloseLead closes new logins this long before maintenance starts, so
// players who are already in finish while nobody new enters.
const loginCloseLead = time.Minute

type Options struct {
	Config  Config
	DataDir string
	// StateDirs are copied before a release that requires a backup.
	StateDirs         []string
	FingerprintID     string
	ServerVersion     string
	GameVersion       string
	BundleVersion     string
	GameDataVersion   string
	CurrentSchedules  string
	ValidateSchedules func(string) error
	// ServeArgs are the running serve flags, reused to preflight a staged package.
	ServeArgs []string
	Board     *notice.Board
	// BundledChangelog is the CHANGELOG.md shipped with this server package.
	BundledChangelog string
	// Shutdown starts the same graceful drain as a termination signal.
	Shutdown func()
	Now      func() time.Time
	Client   *http.Client
}

type Service struct {
	options Options
	keys    []release.TrustedKey

	mu               sync.Mutex
	stageMu          sync.Mutex
	status           Status
	logged           string
	changelogFailure string
	cancelMaint      context.CancelFunc
	plan             *Plan
	wake             chan struct{}
}

func New(options Options) *Service {
	if options.Now == nil {
		options.Now = time.Now
	}
	s := &Service{options: options, keys: release.BuiltinChannel().TrustedKeys, wake: make(chan struct{}, 1)}
	s.status = Status{CurrentVersion: options.ServerVersion, Method: options.Config.Method, CheckEnabled: options.Config.Check, Countdown: options.Config.CountdownMinutes, ManifestURL: options.Config.manifestURL()}
	s.status.Schedules = &ScheduleStatus{CurrentID: options.CurrentSchedules, GameVersion: options.GameVersion, GameDataVersion: options.GameDataVersion}
	var previous Status
	if err := readJSON(filepath.Join(directory(options.DataDir), stateFile), &previous); err == nil {
		s.status.Notified = previous.Notified
		s.status.LastResult = previous.LastResult
		s.status.LastCheck = previous.LastCheck
		if previous.Schedules != nil {
			s.status.Schedules.LastResult = previous.Schedules.LastResult
		}
	}
	return s
}

func newer(current, candidate string) bool {
	ok, err := release.Newer(current, candidate)
	return err == nil && ok
}

// Status returns a snapshot.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.status
	if out.Schedules != nil {
		copy := *out.Schedules
		if copy.Available != nil {
			a := *copy.Available
			copy.Available = &a
		}
		out.Schedules = &copy
	}
	if out.Available != nil {
		copy := *out.Available
		out.Available = &copy
	}
	if out.Maintenance != nil {
		copy := *out.Maintenance
		out.Maintenance = &copy
	}
	mergeHostResult(s.options.DataDir, &out)
	mergeScheduleResult(s.options.DataDir, &out)
	return out
}

func (s *Service) persistLocked() {
	mergeScheduleResult(s.options.DataDir, &s.status)
	s.status.UpdatedAt = s.options.Now().UTC()
	if err := writeJSON(filepath.Join(directory(s.options.DataDir), stateFile), s.status); err != nil {
		slog.Warn("update status could not be saved", "error", err)
	}
}

// Run loads the bundled changelog, then checks at the configured interval
// with jitter and serves CLI control requests until ctx ends.
func (s *Service) Run(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.cancelMaint != nil {
			s.cancelMaint()
		}
	}()
	s.loadBundledChangelog()
	s.mu.Lock()
	s.persistLocked()
	s.mu.Unlock()
	controls := time.NewTicker(5 * time.Second)
	defer controls.Stop()
	next := time.NewTimer(time.Duration(30+rand.IntN(90)) * time.Second)
	defer next.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-controls.C:
			s.serveControl(ctx)
		case <-s.wake:
			s.check(ctx)
		case <-next.C:
			s.check(ctx)
			interval := s.options.Config.Interval()
			next.Reset(interval + time.Duration(rand.Int64N(int64(interval/10)+1)))
		}
	}
}

// CheckNow asks Run to check as soon as possible.
func (s *Service) CheckNow() {
	s.mu.Lock()
	if s.status.Checking {
		s.mu.Unlock()
		return
	}
	s.status.Checking = true
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) loadBundledChangelog() {
	if !s.options.Config.Changelog.Notices || s.options.BundledChangelog == "" {
		return
	}
	raw, err := os.ReadFile(s.options.BundledChangelog)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("bundled changelog unavailable", "error", err)
		}
		return
	}
	signature, err := os.ReadFile(s.options.BundledChangelog + ".sig")
	if err != nil {
		s.reportChangelogFailure("bundled changelog has no signature")
		return
	}
	s.publishChangelog(raw, signature, "bundled")
}

// publishChangelog shows the changelog as official notices only when a
// trusted release key signed exactly these bytes, so nobody else can publish
// notices in our name.
func (s *Service) publishChangelog(raw, signature []byte, source string) {
	if err := release.VerifyChangelog(raw, signature, s.keys); err != nil {
		s.reportChangelogFailure(fmt.Sprintf("%s changelog: %v", source, err))
		return
	}
	entries, err := release.ParseChangelog(raw)
	if err != nil {
		s.reportChangelogFailure(fmt.Sprintf("%s changelog: %v", source, err))
		return
	}
	s.options.Board.Replace(notice.SourceChangelog, notice.FromChangelog(entries, s.options.ServerVersion))
}

// reportChangelogFailure logs a changelog problem once until it changes.
func (s *Service) reportChangelogFailure(message string) {
	s.mu.Lock()
	repeated := s.changelogFailure == message
	s.changelogFailure = message
	s.mu.Unlock()
	if !repeated {
		slog.Warn("changelog notices not refreshed; keeping the previous ones", "reason", message)
	}
}

func (s *Service) check(ctx context.Context) {
	s.mu.Lock()
	s.status.Checking = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.status.Checking = false
		s.status.LastCheck = s.options.Now().UTC()
		s.persistLocked()
		s.mu.Unlock()
	}()
	if s.options.Config.Changelog.Notices {
		fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		raw, signature, source, err := release.FetchSigned(fetchCtx, s.options.Client, s.options.Config.changelogURLs(), s.keys)
		cancel()
		if err != nil {
			s.reportChangelogFailure(err.Error())
		} else {
			s.publishChangelog(raw, signature, source)
		}
	}
	if !s.options.Config.Check {
		return
	}
	address := s.options.Config.manifestURL()
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	manifest, signed, err := release.FetchManifest(fetchCtx, s.options.Client, address, s.keys)
	cancel()
	s.mu.Lock()
	s.status.LastCheck = s.options.Now().UTC()
	if err != nil {
		changed := s.status.LastError != err.Error()
		s.status.LastError = err.Error()
		s.persistLocked()
		s.mu.Unlock()
		if changed {
			slog.Warn("update check failed", "error", err)
		}
		return
	}
	s.status.LastError = ""
	if s.status.Maintenance != nil {
		s.persistLocked()
		s.mu.Unlock()
		return
	}
	schedule := s.updateSchedulesLocked(manifest, signed, address)
	server := manifest.Server
	resourceRevision := server != nil && server.Version == s.options.ServerVersion && manifest.Fingerprint != nil &&
		s.options.FingerprintID != "" && manifest.Fingerprint.ID != s.options.FingerprintID && manifest.Fingerprint.RequireGameUpdate && manifest.Fingerprint.Game != nil &&
		manifest.Fingerprint.Game.GameVersion == s.options.GameVersion &&
		manifest.Fingerprint.Game.GameDataVersion >= s.options.GameDataVersion && manifest.Fingerprint.Game.BundleVersion >= s.options.BundleVersion &&
		(manifest.Fingerprint.Game.GameDataVersion > s.options.GameDataVersion || manifest.Fingerprint.Game.BundleVersion > s.options.BundleVersion)
	if server == nil || !newer(s.options.ServerVersion, server.Version) && !resourceRevision {
		s.status.Available = nil
		s.persistLocked()
		s.mu.Unlock()
		s.preDownloadSchedules(ctx, schedule)
		return
	}
	if s.status.Available == nil || s.status.Available.Version != server.Version ||
		manifest.Fingerprint != nil && (s.status.Available.Fingerprint == nil || s.status.Available.Fingerprint.ID != manifest.Fingerprint.ID) {
		s.status.Available = &Available{Version: server.Version, Date: server.Date, Notes: server.Notes, PublishedAt: manifest.PublishedAt, Commit: manifest.Commit, Image: server.Image, Migration: *server.Migration, Component: server, ManifestURL: address, Fingerprint: manifest.Fingerprint}
	}
	identity := server.Version
	if manifest.Fingerprint != nil {
		identity += ":" + manifest.Fingerprint.ID
	}
	if s.status.Available.Fingerprint != nil && s.status.Available.Fingerprint.Game != nil {
		s.status.Available.GameDataVersion = s.status.Available.Fingerprint.Game.GameDataVersion
	}
	announce := s.logged != identity
	s.logged = identity
	notify := s.status.Notified != identity
	s.persistLocked()
	s.mu.Unlock()
	s.preDownloadSchedules(ctx, schedule)
	if announce {
		slog.Info("server update available", "current", s.options.ServerVersion, "available", server.Version, "published_at", manifest.PublishedAt, "migration", server.Migration.Kind,
			"apply", "admin console or `bd2server update apply`")
	}
	if notify {
		s.sendWebhook(ctx, server, manifest.PublishedAt, identity)
	}
	if s.options.Config.Download && s.options.Config.Method != MethodNone {
		if _, err := s.prepareFiles(ctx, s.Status().Available); err != nil {
			slog.Warn("update pre-download failed", "version", server.Version, "error", err)
		}
	}
}

func (s *Service) sendWebhook(ctx context.Context, server *release.Component, published time.Time, identity string) {
	address := s.options.Config.webhook()
	if address != "" {
		body, _ := json.Marshal(map[string]any{
			"event": "server_update_available", "current_version": s.options.ServerVersion, "available_version": server.Version,
			"published_at": published, "migration": server.Migration.Kind, "notes": server.Notes,
		})
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(body))
		if err == nil {
			request.Header.Set("Content-Type", "application/json")
			client := &http.Client{Timeout: 15 * time.Second}
			var response *http.Response
			if response, err = client.Do(request); err == nil {
				_ = response.Body.Close()
				if response.StatusCode >= 300 {
					err = fmt.Errorf("HTTP %d", response.StatusCode)
				}
			}
		}
		if err != nil {
			slog.Warn("update webhook failed", "error", err)
			return
		}
	}
	s.mu.Lock()
	s.status.Notified = identity
	s.persistLocked()
	s.mu.Unlock()
}

// prepareFiles downloads and unpacks the server package for this platform,
// returning the staged directory. Docker deployments need no files.
func (s *Service) prepareFiles(ctx context.Context, available *Available) (string, error) {
	s.stageMu.Lock()
	defer s.stageMu.Unlock()
	if available == nil {
		return "", errors.New("no newer server release is known")
	}
	var staged string
	var err error
	if s.options.Config.Method == MethodDocker {
		if available.Image == "" && available.Commit == "" {
			err = errors.New("the release names no Docker image or commit")
		}
	} else {
		staged, err = stage(ctx, s.options.Client, available, s.options.DataDir)
	}
	if err == nil && available.Fingerprint != nil && available.Fingerprint.RequireGameUpdate && available.Fingerprint.Game != nil {
		g := available.Fingerprint.Game
		_, err = resourcefetch.Fetch(ctx, resourcefetch.Options{OutputRoot: ResourceDirectory(s.options.DataDir, *g), Client: s.options.Client, Pinned: g})
	}
	s.mu.Lock()
	if s.status.Available != nil && candidateIdentity(s.status.Available) == candidateIdentity(available) {
		s.status.Available.Downloaded = err == nil
		s.status.Available.DownloadError = ""
		if err != nil {
			s.status.Available.DownloadError = err.Error()
		}
		s.persistLocked()
	}
	s.mu.Unlock()
	return staged, err
}

func candidateIdentity(a *Available) string {
	if a.Fingerprint != nil {
		return a.Version + "-" + a.Fingerprint.ID
	}
	return a.Version
}

func stage(ctx context.Context, client *http.Client, available *Available, dataDir string) (string, error) {
	var archive *release.File
	for _, f := range available.Component.FilesFor(release.Platform()) {
		if f.Platform != "" {
			copy := f
			archive = &copy
		}
	}
	if archive == nil {
		return "", fmt.Errorf("the release has no server package for %s", release.Platform())
	}
	downloads := filepath.Join(directory(dataDir), "downloads", candidateIdentity(available))
	path, err := release.Download(ctx, client, available.ManifestURL, *archive, downloads, nil)
	if err != nil {
		return "", err
	}
	install, err := InstallDirectory()
	if err != nil {
		return "", err
	}
	target := filepath.Join(install, ".update-staged", candidateIdentity(available))
	temporary := target + ".tmp"
	if err := os.RemoveAll(temporary); err != nil {
		return "", err
	}
	if err := release.Extract(path, temporary); err != nil {
		_ = os.RemoveAll(temporary)
		return "", fmt.Errorf("unpack server package: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	// Packages hold one top-level bd2server directory.
	root := filepath.Join(temporary, "bd2server")
	if available.Fingerprint != nil {
		f, err := release.LoadFingerprint(filepath.Join(root, release.FingerprintFileName), release.BuiltinChannel().TrustedKeys)
		if err != nil {
			return "", err
		}
		if f.ID != available.Fingerprint.ID {
			return "", errors.New("server archive does not match the signed release fingerprint")
		}
	}
	executable := filepath.Join(root, executableName())
	if info, err := os.Stat(executable); err != nil || !info.Mode().IsRegular() {
		return "", errors.New("the server package has no bd2server executable")
	}
	if err := os.Chmod(executable, 0o755); err != nil {
		return "", err
	}
	if err := os.RemoveAll(target); err != nil {
		return "", err
	}
	return target, os.Rename(root, target)
}

// Schedule prepares the available release and, once it is ready, warns
// players and starts maintenance after delay.
func (s *Service) Schedule(delay time.Duration, confirmIrreversible bool, by string) error {
	return s.schedule(ComponentServer, delay, confirmIrreversible, by)
}

func (s *Service) ScheduleSchedules(delay time.Duration, by string) error {
	return s.schedule(ComponentSchedules, delay, false, by)
}

func (s *Service) schedule(component string, delay time.Duration, confirmIrreversible bool, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	available := s.status.Available
	var schedule *ScheduleAvailable
	if component == ComponentSchedules {
		schedule = s.status.Schedules.Available
	}
	switch {
	case s.options.Config.Method == MethodNone:
		return reject(RejectNotifyOnly, "the install method is notify-only")
	case component == ComponentServer && available == nil, component == ComponentSchedules && schedule == nil:
		return reject(RejectNoUpdate, "no newer server release is available")
	case s.status.Maintenance != nil:
		return reject(RejectAlreadyScheduled, "a maintenance is already scheduled")
	case component == ComponentSchedules && schedule.Incompatible != "":
		return reject("incompatible_schedules", "%s", schedule.Incompatible)
	case component == ComponentServer && available.Migration.Kind == release.MigrationIrreversible && !confirmIrreversible:
		return reject(RejectIrreversible, "%s contains a save migration that cannot be rolled back: %s; confirm it explicitly", available.Version, available.Migration.Note.In(i18n.Fallback))
	case delay < 0 || delay > 24*time.Hour:
		return reject(RejectInvalidDelay, "the countdown must be between 0 and 24 hours")
	}
	for _, file := range []string{pendingFile, schedulePendingFile} {
		if _, err := os.Stat(filepath.Join(directory(s.options.DataDir), file)); err == nil {
			return reject("confirming_update", "the previous update is still being confirmed; retry once it is healthy")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if component == ComponentServer && s.options.Config.Method == MethodNative {
		if err := checkWritable(); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelMaint = cancel
	version := ""
	if component == ComponentSchedules {
		version = schedule.Component.ID
	} else {
		version = available.Version
	}
	s.status.Maintenance = &Maintenance{Component: component, Version: version, Phase: PhasePreparing, RequestedBy: by, Delay: int(delay / time.Minute)}
	s.persistLocked()
	slog.Info("update scheduled", "component", component, "version", version, "delay", delay, "by", by)
	go s.runMaintenance(ctx, available, schedule, delay)
	return nil
}

func (s *Service) runMaintenance(ctx context.Context, available *Available, schedule *ScheduleAvailable, delay time.Duration) {
	var staged, version string
	var plan *Plan
	var err error
	if schedule != nil {
		version = schedule.Component.ID
		_, err = s.prepareSchedules(ctx, schedule)
		plan = &Plan{DataDir: s.options.DataDir, From: s.options.CurrentSchedules, To: version, Method: s.options.Config.Method,
			Schedules: &SchedulePlan{ID: version, GameVersion: s.options.GameVersion, GameDataVersion: s.options.GameDataVersion}}
	} else {
		version = available.Version
		staged, err = s.prepareFiles(ctx, available)
		if err == nil && s.options.Config.Method == MethodNative {
			err = s.preflight(ctx, staged)
		}
		plan = &Plan{DataDir: s.options.DataDir, From: s.options.ServerVersion, To: version, Method: s.options.Config.Method, Staged: staged, Migration: available.Migration, Image: available.Image, Commit: available.Commit, StateDirs: s.options.StateDirs}
		if available.Fingerprint != nil {
			plan.FingerprintID = available.Fingerprint.ID
			plan.FromFingerprintID = s.options.FingerprintID
		}
	}
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("update preparation failed; maintenance cancelled", "version", version, "error", err)
			s.finishMaintenance(ctx, fmt.Sprintf("preparation failed: %v", err))
		}
		return
	}
	at := s.options.Now().Add(delay).Truncate(time.Second)
	s.mu.Lock()
	if s.status.Maintenance == nil || ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	s.status.Maintenance.Phase = PhaseCountdown
	s.status.Maintenance.At = at.UTC()
	s.persistLocked()
	s.publishMaintenance(version, at)
	s.mu.Unlock()
	slog.Info("maintenance countdown started", "version", version, "at", at.UTC())
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	s.mu.Lock()
	if s.status.Maintenance == nil || ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	s.status.Maintenance.Phase = PhaseClosing
	s.plan = plan
	s.persistLocked()
	s.mu.Unlock()
	slog.Info("maintenance started; draining for update", "version", version)
	s.options.Shutdown()
}

func (s *Service) preflight(ctx context.Context, staged string) error {
	args := []string{"preflight", "--data-dir", s.options.DataDir, "--version-config", filepath.Join(staged, "versions.json")}
	skip := false
	for _, arg := range s.options.ServeArgs {
		if skip {
			skip = false
			continue
		}
		name, _, assigned := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if name == "data-dir" || name == "version-config" || name == "listen" {
			skip = !assigned
			continue
		}
		args = append(args, arg)
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	command := exec.CommandContext(runCtx, filepath.Join(staged, executableName()), args...)
	output, err := command.CombinedOutput()
	if err != nil {
		tail := strings.TrimSpace(string(output))
		if len(tail) > 600 {
			tail = tail[len(tail)-600:]
		}
		return fmt.Errorf("the new version failed preflight: %v: %s", err, tail)
	}
	return nil
}

func (s *Service) publishMaintenance(version string, at time.Time) {
	minutes := s.options.Config.MaintenanceMinutes
	now := s.options.Now()
	// Changelog IDs stay below 0x40000000; each maintenance gets fresh IDs so
	// the client's read marks from an earlier one do not hide it.
	id := int32(0x40000000 | (at.Unix()/60&0x0fffffff)<<1)
	banner := notice.Notice{ID: id, Type: notice.TypeEmergency, Start: now, End: at, Text: map[string]notice.Content{}}
	detail := notice.Notice{ID: id + 1, Type: notice.TypeNotice, Start: now, End: at.Add(time.Duration(minutes) * time.Minute), Pinned: true, Text: map[string]notice.Content{}}
	for _, locale := range i18n.Locales {
		title := texts.Translate(locale, "maintenance.title")
		prefix, target := "maintenance.", version
		if s.status.Maintenance != nil && s.status.Maintenance.Component == ComponentSchedules {
			prefix, target = "maintenance.schedules.", version[:12]
		}
		banner.Text[locale] = notice.Content{Title: title, Blocks: []notice.Block{{Value: maintenanceText(locale, prefix+"banner", target, at, minutes)}}}
		detail.Text[locale] = notice.Content{Title: title, Blocks: []notice.Block{{Value: maintenanceText(locale, prefix+"body", target, at, minutes)}}}
	}
	s.options.Board.Replace(SourceMaintenance, []notice.Notice{banner, detail})
}

// Cancel stops a maintenance that has not started draining yet.
func (s *Service) Cancel(by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Maintenance == nil {
		return reject(RejectNoMaintenance, "no maintenance is scheduled")
	}
	if s.status.Maintenance.Phase == PhaseClosing {
		return reject(RejectMaintenanceClosing, "the maintenance is already draining and cannot be cancelled")
	}
	slog.Info("server update maintenance cancelled", "by", by)
	s.finishMaintenanceLocked("")
	return nil
}

func (s *Service) finishMaintenance(ctx context.Context, failure string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	s.finishMaintenanceLocked(failure)
}

func (s *Service) finishMaintenanceLocked(failure string) {
	if s.cancelMaint != nil {
		s.cancelMaint()
		s.cancelMaint = nil
	}
	if s.status.Maintenance != nil && failure != "" {
		result := &Result{From: s.options.ServerVersion, To: s.status.Maintenance.Version, Outcome: "cancelled", Message: failure, At: s.options.Now().UTC()}
		if s.status.Maintenance.Component == ComponentSchedules {
			result.From = s.options.CurrentSchedules
			s.status.Schedules.LastResult = result
		} else {
			s.status.LastResult = result
		}
	}
	s.status.Maintenance = nil
	s.persistLocked()
	s.options.Board.Replace(SourceMaintenance, nil)
}

// MaintenanceStart is the Notify.maintenance_start_date value, or zero.
func (s *Service) MaintenanceStart() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.status.Maintenance; m != nil && !m.At.IsZero() {
		return m.At.UnixMilli()
	}
	return 0
}

// LoginsClosed reports whether new logins are refused, and the message the
// title screen shows in locale.
func (s *Service) LoginsClosed(locale string) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.status.Maintenance
	if m == nil || m.At.IsZero() || s.options.Now().Before(m.At.Add(-loginCloseLead)) {
		return false, ""
	}
	key, target := "maintenance.login", m.Version
	if m.Component == ComponentSchedules {
		key, target = "maintenance.schedules.login", m.Version[:12]
	}
	return true, maintenanceText(locale, key, target, m.At, s.options.Config.MaintenanceMinutes)
}

// TakePlan returns the switch to perform after the drained server stopped.
func (s *Service) TakePlan() *Plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan := s.plan
	s.plan = nil
	return plan
}

func (s *Service) serveControl(ctx context.Context) {
	path := filepath.Join(directory(s.options.DataDir), controlFile)
	var control Control
	if err := readJSON(path, &control); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(path)
		}
		return
	}
	_ = os.Remove(path)
	ack := ControlAck{ID: control.ID, OK: true}
	switch control.Action {
	case ActionCheck:
		s.check(ctx)
		status := s.Status()
		ack.Message = "checked"
		if status.LastError != "" {
			ack.OK, ack.Message = false, status.LastError
		} else if status.Available != nil {
			ack.Message = "new version available: " + status.Available.Version
		} else if status.Schedules != nil && status.Schedules.Available != nil {
			ack.Message = "schedule update available: " + status.Schedules.Available.Component.ID
			if message := status.Schedules.Available.Incompatible; message != "" {
				ack.Message += " (incompatible: " + message + ")"
			}
		} else {
			ack.Message = "already up to date"
		}
	case ActionApply:
		var err error
		switch control.Component {
		case "", ComponentServer:
			err = s.Schedule(time.Duration(control.DelayMinutes)*time.Minute, control.ConfirmIrreversible, "cli")
		case ComponentSchedules:
			err = s.ScheduleSchedules(time.Duration(control.DelayMinutes)*time.Minute, "cli")
		default:
			err = errors.New("unknown update component")
		}
		if err != nil {
			ack.OK, ack.Message = false, err.Error()
		} else {
			ack.Message = fmt.Sprintf("maintenance scheduled; it starts %d minutes after the release is prepared", control.DelayMinutes)
		}
	case ActionCancel:
		if err := s.Cancel("cli"); err != nil {
			ack.OK, ack.Message = false, err.Error()
		} else {
			ack.Message = "maintenance cancelled"
		}
	default:
		ack.OK, ack.Message = false, "unknown action "+control.Action
	}
	s.mu.Lock()
	s.status.Control = &ack
	s.persistLocked()
	s.mu.Unlock()
}

// Rejection codes let the console translate why an update request failed.
const (
	RejectNotifyOnly         = "notify_only"
	RejectNoUpdate           = "no_update"
	RejectAlreadyScheduled   = "already_scheduled"
	RejectIrreversible       = "irreversible_unconfirmed"
	RejectInvalidDelay       = "invalid_delay"
	RejectNotWritable        = "install_not_writable"
	RejectNoMaintenance      = "no_maintenance"
	RejectMaintenanceClosing = "maintenance_closing"
)

// Rejection is a refused update request.
type Rejection struct {
	Code    string
	Message string
}

func (r *Rejection) Error() string { return r.Message }

func reject(code, format string, args ...any) error {
	return &Rejection{Code: code, Message: fmt.Sprintf(format, args...)}
}
