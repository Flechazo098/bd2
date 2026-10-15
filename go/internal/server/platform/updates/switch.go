package updates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"bd2server/internal/common/release"
)

// Exit codes after a switch. systemd units restart on ExitRestart and must
// list ExitNeedsOperator in RestartPreventExitStatus.
const (
	ExitRestart       = 75
	ExitNeedsOperator = 78
)

// maxStartAttempts bounds how often a new version may fail to come up before
// its program files are rolled back.
const maxStartAttempts = 3

// healthyAfter is how long a new version must serve before the switch counts
// as successful and the rollback marker is removed.
const healthyAfter = 90 * time.Second

// hostWait is how long a Docker container waits at start for the host script
// to replace it after it requested a new image.
const hostWait = 10 * time.Minute

// Plan is a switch decided by the running server and performed after it has
// drained, saved and closed every store.
type Plan struct {
	DataDir   string
	From, To  string
	Method    string
	Staged    string
	Migration release.Migration
	Image     string
	Commit    string
	// StateDirs are backed up first when the release requires a backup.
	StateDirs []string
}

// RestartError ends Serve when it stopped for an update.
type RestartError struct{ Plan *Plan }

func (e *RestartError) Error() string { return "server stopped to update to " + e.Plan.To }

type pending struct {
	Install   string            `json:"install"`
	From      string            `json:"from"`
	To        string            `json:"to"`
	Names     []string          `json:"names"`
	Rollback  string            `json:"rollback"`
	Migration release.Migration `json:"migration"`
	Attempts  int               `json:"attempts"`
	At        time.Time         `json:"at"`
}

type dockerRequest struct {
	From      string            `json:"from"`
	To        string            `json:"to"`
	Image     string            `json:"image,omitempty"`
	Commit    string            `json:"commit"`
	Migration release.Migration `json:"migration"`
	At        time.Time         `json:"at"`
}

func executableName() string {
	if runtime.GOOS == "windows" {
		return "bd2server.exe"
	}
	return "bd2server"
}

// InstallDirectory is the directory holding the running executable.
func InstallDirectory() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	return filepath.Dir(executable), nil
}

func checkWritable() error {
	install, err := InstallDirectory()
	if err != nil {
		return err
	}
	probe, err := os.CreateTemp(install, ".update-probe-*")
	if err != nil {
		return reject(RejectNotWritable, "the install directory %s is not writable: %v", install, err)
	}
	name := probe.Name()
	_ = probe.Close()
	return os.Remove(name)
}

// Execute backs up state when required and hands over to the new version.
// On failure the current program stays installed and the outcome is recorded.
func (p *Plan) Execute(now time.Time) error {
	err := p.execute(now)
	if err != nil {
		recordResult(p.DataDir, Result{From: p.From, To: p.To, Outcome: OutcomeSwitchFailed, Message: err.Error(), At: now.UTC()})
	}
	return err
}

func (p *Plan) execute(now time.Time) error {
	if p.Migration.Backup {
		target := filepath.Join(p.DataDir, "backups", fmt.Sprintf("update-%s-%s-to-%s", now.UTC().Format("20060102T150405Z"), p.From, p.To))
		if err := backup(p.StateDirs, target); err != nil {
			return fmt.Errorf("backup before update: %w", err)
		}
		slog.Info("state backed up before update", "path", target)
	}
	if p.Method == MethodDocker {
		request := dockerRequest{From: p.From, To: p.To, Image: p.Image, Commit: p.Commit, Migration: p.Migration, At: now.UTC()}
		if err := writeJSON(filepath.Join(directory(p.DataDir), dockerFile), request); err != nil {
			return err
		}
		slog.Info("update requested from the Docker host", "image", p.Image, "commit", p.Commit)
		return nil
	}
	install, err := InstallDirectory()
	if err != nil {
		return err
	}
	rollback := filepath.Join(install, ".update-rollback", p.From)
	names, err := swap(install, p.Staged, rollback)
	if err != nil {
		return err
	}
	record := pending{Install: install, From: p.From, To: p.To, Names: names, Rollback: rollback, Migration: p.Migration, At: now.UTC()}
	if err := writeJSON(filepath.Join(directory(p.DataDir), pendingFile), record); err != nil {
		if restoreErr := restore(install, rollback, names); restoreErr != nil {
			return errors.Join(err, restoreErr)
		}
		return err
	}
	_ = os.Remove(p.Staged)
	slog.Info("program files switched; restarting into the new version", "from", p.From, "to", p.To)
	return nil
}

// swap moves the installed copies of every staged top-level entry aside and
// moves the staged ones in. data/ is never part of a program switch.
func swap(install, staged, rollback string) ([]string, error) {
	entries, err := os.ReadDir(staged)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.Name() != "data" {
			names = append(names, entry.Name())
		}
	}
	if err := os.RemoveAll(rollback); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(rollback, 0o755); err != nil {
		return nil, err
	}
	var moved, placed []string
	undo := func(cause error) ([]string, error) {
		for _, name := range placed {
			_ = os.Rename(filepath.Join(install, name), filepath.Join(staged, name))
		}
		for _, name := range moved {
			_ = os.Rename(filepath.Join(rollback, name), filepath.Join(install, name))
		}
		return nil, fmt.Errorf("switch program files (restored the previous version): %w", cause)
	}
	for _, name := range names {
		if _, err := os.Lstat(filepath.Join(install, name)); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := os.Rename(filepath.Join(install, name), filepath.Join(rollback, name)); err != nil {
			return undo(err)
		}
		moved = append(moved, name)
	}
	for _, name := range names {
		if err := os.Rename(filepath.Join(staged, name), filepath.Join(install, name)); err != nil {
			return undo(err)
		}
		placed = append(placed, name)
	}
	return names, nil
}

func restore(install, rollback string, names []string) error {
	var failures []error
	for _, name := range names {
		if err := os.RemoveAll(filepath.Join(install, name)); err != nil {
			failures = append(failures, err)
		}
		if err := os.Rename(filepath.Join(rollback, name), filepath.Join(install, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Startup runs before the server opens its stores. It counts start attempts
// of a freshly switched version and, for Docker, waits for a requested host
// switch. A returned error must stop the process with the returned exit code.
func Startup(ctx context.Context, dataDir, version string) (*Confirmation, int, error) {
	if hold, err := awaitHost(ctx, dataDir, version); hold || err != nil {
		var exit *ExitError
		if errors.As(err, &exit) {
			return nil, exit.Code, exit
		}
		return nil, ExitRestart, err
	}
	path := filepath.Join(directory(dataDir), pendingFile)
	var record pending
	if err := readJSON(path, &record); errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	} else if err != nil {
		return nil, 0, err
	}
	if record.To != version {
		// A rolled-back program starts here: the record is stale.
		_ = os.Remove(path)
		return nil, 0, nil
	}
	record.Attempts++
	if record.Attempts > maxStartAttempts {
		code, err := rollBack(dataDir, record, fmt.Sprintf("it failed to start %d times in a row", maxStartAttempts))
		return nil, code, err
	}
	if err := writeJSON(path, record); err != nil {
		return nil, 0, err
	}
	return &Confirmation{dataDir: dataDir, record: record}, 0, nil
}

// Confirmation finishes a switch once the new version has been healthy.
type Confirmation struct {
	dataDir string
	record  pending
	mu      sync.Mutex
	settled bool
}

// AfterHealthy marks the switch successful once ready has stayed true for a
// while, keeping only the most recent rollback copy.
func (c *Confirmation) AfterHealthy(ctx context.Context, ready func() bool) {
	if c == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	c.observeHealthy(ctx, ready, ticker.C)
}

func (c *Confirmation) observeHealthy(ctx context.Context, ready func() bool, ticks <-chan time.Time) {
	var since time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticks:
			if !ready() {
				since = time.Time{}
				continue
			}
			if since.IsZero() {
				since = now
			}
			if now.Sub(since) >= healthyAfter {
				goto healthy
			}
		}
	}
healthy:
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settled || ctx.Err() != nil {
		return
	}
	c.settled = true
	recordResult(c.dataDir, Result{From: c.record.From, To: c.record.To, Outcome: OutcomeSucceeded, At: time.Now().UTC()})
	_ = os.Remove(filepath.Join(directory(c.dataDir), pendingFile))
	parent := filepath.Dir(c.record.Rollback)
	if entries, err := os.ReadDir(parent); err == nil {
		for _, entry := range entries {
			if entry.Name() != filepath.Base(c.record.Rollback) {
				_ = os.RemoveAll(filepath.Join(parent, entry.Name()))
			}
		}
	}
	slog.Info("server update confirmed healthy", "from", c.record.From, "to", c.record.To)
}

// Failed handles a start of a freshly switched version that ended in error.
// It returns the exit code the process should use.
func (c *Confirmation) Failed(cause error) (int, error) {
	if c == nil {
		return 1, cause
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settled {
		return 1, cause
	}
	c.settled = true
	return rollBack(c.dataDir, c.record, cause.Error())
}

func rollBack(dataDir string, record pending, reason string) (int, error) {
	if record.Migration.Kind == release.MigrationIrreversible {
		message := fmt.Sprintf("%s failed to start (%s); it has an irreversible save migration, so the previous program was not restored automatically; follow the release notes", record.To, reason)
		recordResult(dataDir, Result{From: record.From, To: record.To, Outcome: OutcomeNeedsOperator, Message: message, At: time.Now().UTC()})
		return ExitNeedsOperator, errors.New(message)
	}
	if err := restore(record.Install, record.Rollback, record.Names); err != nil {
		message := fmt.Sprintf("%s failed to start (%s) and restoring the previous program failed: %v", record.To, reason, err)
		recordResult(dataDir, Result{From: record.From, To: record.To, Outcome: OutcomeNeedsOperator, Message: message, At: time.Now().UTC()})
		return ExitNeedsOperator, errors.New(message)
	}
	_ = os.Remove(filepath.Join(directory(dataDir), pendingFile))
	message := fmt.Sprintf("%s failed to start (%s); the %s program was restored; saves were not rolled back", record.To, reason, record.From)
	recordResult(dataDir, Result{From: record.From, To: record.To, Outcome: OutcomeRolledBack, Message: message, At: time.Now().UTC()})
	return ExitRestart, errors.New(message)
}

func recordResult(dataDir string, result Result) {
	path := filepath.Join(directory(dataDir), stateFile)
	var status Status
	_ = readJSON(path, &status)
	status.LastResult = &result
	status.UpdatedAt = time.Now().UTC()
	if err := writeJSON(path, status); err != nil {
		slog.Warn("update result could not be saved", "error", err)
	}
}

// awaitHost holds a container that requested a new image until the host
// replaces it, and records the outcome when the new image starts.
func awaitHost(ctx context.Context, dataDir, version string) (bool, error) {
	path := filepath.Join(directory(dataDir), dockerFile)
	var request dockerRequest
	if err := readJSON(path, &request); errors.Is(err, os.ErrNotExist) {
		path = filepath.Join(directory(dataDir), "docker-request.processing.json")
		if err = readJSON(path, &request); errors.Is(err, os.ErrNotExist) {
			return false, nil
		} else if err != nil {
			return true, err
		}
	} else if err != nil {
		return true, err
	}
	if request.To == version {
		return false, nil
	}
	if version != request.From {
		return true, errors.New("the running program does not match either version of the Docker update request")
	}
	deadline := request.At.Add(hostWait)
	slog.Info("waiting for the Docker host to finish the update", "version", request.To)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		var result hostResult
		if err := readJSON(filepath.Join(directory(dataDir), "docker-result.json"), &result); err == nil && result.From == request.From && result.To == request.To && result.RequestAt.Equal(request.At) {
			recordResult(dataDir, result.Result)
			switch result.Outcome {
			case OutcomeRolledBack, OutcomeSwitchFailed:
				return false, nil
			default:
				return true, &ExitError{Code: ExitNeedsOperator, Err: errors.New("the Docker host requires operator attention before starting this version")}
			}
		}
		if !time.Now().Before(deadline) {
			message := "the Docker host did not finish the update within 10 minutes; operator attention is required"
			recordResult(dataDir, Result{From: request.From, To: request.To, Outcome: OutcomeNeedsOperator, Message: message, At: time.Now().UTC()})
			return true, &ExitError{Code: ExitNeedsOperator, Err: errors.New(message)}
		}
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-ticker.C:
		}
	}
}

type hostResult struct {
	Result
	RequestAt time.Time `json:"request_at"`
}

// backup copies every directory into target and verifies each file.
func backup(directories []string, target string) error {
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	for i, source := range directories {
		destination := filepath.Join(target, fmt.Sprintf("%d-%s", i, filepath.Base(source)))
		err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			out := filepath.Join(destination, relative)
			if entry.IsDir() {
				return os.MkdirAll(out, 0o700)
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			return copyVerified(path, out)
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func copyVerified(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	digest := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, digest), in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	copied, err := release.HashFile(target)
	if err != nil {
		return err
	}
	if want := hex.EncodeToString(digest.Sum(nil)); copied != want {
		return fmt.Errorf("backup of %s does not match its source", source)
	}
	return nil
}

// ExitError carries the process exit code an update decision requires.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }
