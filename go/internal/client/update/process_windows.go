//go:build windows

package update

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// runningExecutables returns the full image paths of running processes,
// lower-cased so the main client and a second instance stay distinct even
// though both run "BrownDust II.exe".
func runningExecutables() (map[string]bool, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for {
		if path, ok := imagePath(entry.ProcessID); ok {
			out[strings.ToLower(filepath.Clean(path))] = true
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return out, nil
			}
			return nil, err
		}
	}
}

func imagePath(pid uint32) (string, bool) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", false
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return "", false
	}
	return windows.UTF16ToString(buffer[:size]), true
}

func executableKey(path string) string { return strings.ToLower(filepath.Clean(path)) }

// waitForExit blocks until pid has exited or timeout passes.
func waitForExit(pid int, timeout time.Duration) error {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	event, err := windows.WaitForSingleObject(handle, uint32(timeout/time.Millisecond))
	if err != nil {
		return err
	}
	if event == uint32(windows.WAIT_TIMEOUT) {
		return errors.New("the launcher did not exit in time")
	}
	return nil
}

// startDetached starts a process that outlives the launcher.
func startDetached(name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Dir = filepath.Dir(name)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
	command.Env = os.Environ()
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

// helperRequired: Windows cannot replace a running executable, so a copy of
// the launcher finishes the switch after this process exits.
const helperRequired = true
