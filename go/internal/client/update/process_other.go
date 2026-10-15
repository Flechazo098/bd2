//go:build !windows

package update

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// runningExecutables lists the full executable paths of running processes.
// On macOS ps reports the path inside the application bundle.
func runningExecutables() (map[string]bool, error) {
	output, err := exec.Command("ps", "-axww", "-o", "comm=").Output()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			out[executableKey(line)] = true
		}
	}
	return out, scanner.Err()
}

func executableKey(path string) string { return filepath.Clean(path) }

func waitForExit(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		process, err := os.FindProcess(pid)
		if err != nil || process.Signal(syscall.Signal(0)) != nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("the launcher did not exit in time")
}

func startDetached(name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Dir = filepath.Dir(name)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

// helperRequired: Unix renames over a running executable safely, so the
// launcher switches its own files and starts the new copy directly.
const helperRequired = false
