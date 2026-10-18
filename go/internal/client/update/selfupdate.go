package update

import (
	"bd2server/internal/common/util"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"time"

	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/common/release"
)

// HelperFlag starts bd2client as the small updater that finishes a launcher
// switch after the running launcher has exited.
const HelperFlag = "--apply-launcher-update"

const resultFile = "result.json"
const launcherManifestFile = "manifest.json"

// Result is what the next launcher start reports about a launcher switch.
type Result struct {
	From    string    `json:"from"`
	To      string    `json:"to"`
	OK      bool      `json:"ok"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

func launcherExecutable() string {
	if runtime.GOOS == "windows" {
		return "bd2client.exe"
	}
	return "bd2client"
}

// RunHelper is the entry point for HelperFlag: staging target pid from to.
func RunHelper(args []string) error {
	return runHelper(args, release.BuiltinChannel().TrustedKeys, startDetached)
}

func runHelper(args []string, keys []release.TrustedKey, start func(string, ...string) error) error {
	if len(args) != 5 {
		return errors.New("usage: bd2client --apply-launcher-update STAGING TARGET PID FROM TO")
	}
	staging, target, from, to := args[0], args[1], args[3], args[4]
	pid, err := strconv.Atoi(args[2])
	if err != nil || pid <= 0 {
		return errors.New("invalid launcher process ID")
	}
	result := Result{From: from, To: to, At: time.Now().UTC()}
	if err := waitForExit(pid, time.Minute); err != nil {
		result.Message = err.Error()
		return errors.Join(err, writeResult(target, result))
	} else if err := switchLauncher(staging, target, from, to, keys); err != nil {
		result.Message = err.Error()
	} else {
		result.OK = true
	}
	recordErr := writeResult(target, result)
	startErr := start(filepath.Join(target, launcherExecutable()))
	var switchErr error
	if !result.OK {
		switchErr = errors.New(result.Message)
	}
	return errors.Join(switchErr, recordErr, startErr)
}

func (m *Manager) prepareLauncher(staging string, component *release.Component) error {
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	for _, f := range component.FilesFor(release.Platform()) {
		staged := filepath.Join(staging, f.Name)
		if !release.Matches(staged, f) {
			current := filepath.Join(m.options.Directory, f.Name)
			if !release.Matches(current, f) {
				return fmt.Errorf("launcher %s failed verification", f.Name)
			}
			if err := copyFile(current, staged); err != nil {
				return err
			}
			if !release.Matches(staged, f) {
				return fmt.Errorf("launcher %s changed while staging", f.Name)
			}
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(staging, launcherExecutable()), 0o755); err != nil {
			return err
		}
	}
	return commonconfig.WriteFile(filepath.Join(staging, launcherManifestFile), m.envelope)
}

func launcherFiles(staging, to string, keys []release.TrustedKey) ([]release.File, error) {
	raw, err := os.ReadFile(filepath.Join(staging, launcherManifestFile))
	if err != nil {
		return nil, err
	}
	manifest, err := release.Verify(raw, keys)
	if err != nil {
		return nil, err
	}
	if manifest.Channel != release.BuiltinChannel().Name || manifest.Launcher == nil || manifest.Launcher.Version != to {
		return nil, errors.New("launcher manifest does not match this update")
	}
	files := manifest.Launcher.FilesFor(release.Platform())
	if !slices.ContainsFunc(files, func(f release.File) bool { return f.Name == launcherExecutable() }) {
		return nil, errors.New("launcher manifest does not contain the executable")
	}
	for _, f := range files {
		if !release.Matches(filepath.Join(staging, f.Name), f) {
			return nil, fmt.Errorf("launcher %s failed verification", f.Name)
		}
	}
	return files, nil
}

// switchLauncher verifies the signed set again before replacing any files.
func switchLauncher(staging, target, from, to string, keys []release.TrustedKey) error {
	if version, err := release.ParseVersion(from); err != nil || version.Kind != "client" {
		return errors.New("invalid previous launcher version")
	}
	files, err := launcherFiles(staging, to, keys)
	if err != nil {
		return err
	}
	rollback := filepath.Join(target, ".update", "rollback", from)
	if err := os.MkdirAll(rollback, 0o755); err != nil {
		return err
	}
	transaction, err := os.MkdirTemp(rollback, "switch-*")
	if err != nil {
		return err
	}
	type move struct{ name, backup string }
	var done []move
	undo := func(cause error) error {
		var failures []error
		for _, m := range slices.Backward(done) {
			current := filepath.Join(target, m.name)
			if m.backup != "" {
				if err := util.ReplaceFile(m.backup, current); err != nil {
					failures = append(failures, fmt.Errorf("restore %s: %w", m.name, err))
				}
			} else if err := os.Remove(current); err != nil && !errors.Is(err, os.ErrNotExist) {
				failures = append(failures, err)
			}
		}
		if len(failures) != 0 {
			return fmt.Errorf("launcher replacement failed; rollback incomplete (backups in %s): %w", transaction, errors.Join(append([]error{cause}, failures...)...))
		}
		_ = os.RemoveAll(transaction)
		return fmt.Errorf("launcher replacement failed; previous version restored: %w", cause)
	}
	for _, f := range files {
		current := filepath.Join(target, f.Name)
		temporary, err := os.CreateTemp(target, ".launcher-*.new")
		if err != nil {
			return undo(err)
		}
		name := temporary.Name()
		if err := temporary.Close(); err != nil {
			_ = os.Remove(name)
			return undo(err)
		}
		if err := copyFile(filepath.Join(staging, f.Name), name); err != nil {
			_ = os.Remove(name)
			return undo(err)
		}
		if !release.Matches(name, f) {
			_ = os.Remove(name)
			return undo(fmt.Errorf("launcher %s changed during installation", f.Name))
		}
		m := move{name: f.Name}
		info, err := os.Lstat(current)
		if err == nil {
			if !info.Mode().IsRegular() {
				_ = os.Remove(name)
				return undo(fmt.Errorf("launcher target %s is not a regular file", current))
			}
			m.backup = filepath.Join(transaction, f.Name)
			if err := os.Rename(current, m.backup); err != nil {
				_ = os.Remove(name)
				return undo(err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(name)
			return undo(err)
		}
		done = append(done, m)
		if err := util.ReplaceFile(name, current); err != nil {
			_ = os.Remove(name)
			return undo(err)
		}
	}
	return nil
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm()|0o600)
	if err != nil {
		return err
	}
	if err := out.Chmod(info.Mode().Perm() | 0o600); err != nil {
		_ = out.Close()
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func writeResult(target string, result Result) error {
	return commonconfig.Save(filepath.Join(target, ".update", resultFile), result, commonconfig.WriteOptions{})
}

// TakeResult returns and clears the outcome of the last launcher switch.
func TakeResult(target string) *Result {
	path := filepath.Join(target, ".update", resultFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	_ = os.Remove(path)
	var result Result
	if json.Unmarshal(raw, &result) != nil {
		return nil
	}
	return &result
}
