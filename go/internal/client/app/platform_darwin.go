//go:build darwin

package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func ShowFatalError(err error) {
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "BD2 Client Studio:", err)
	}
}

func launchGame(target, proxyURL string) error {
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() || !strings.EqualFold(filepath.Ext(target), ".app") {
		return fmt.Errorf("invalid macOS application bundle %q", target)
	}
	if err := exec.Command("pgrep", "-x", "BrownDust II").Run(); err == nil {
		return errGameAlreadyRunning
	}
	// LaunchServices does not inherit open's environment; --env explicitly
	// supplies the Unity variables to the newly launched application.
	args := gameOpenArguments(target, os.Environ(), proxyURL)
	return exec.Command("open", args...).Start()
}
