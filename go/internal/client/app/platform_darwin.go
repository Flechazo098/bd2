//go:build darwin

package app

import (
	"errors"
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

func launchGame(target string) error {
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() || !strings.EqualFold(filepath.Ext(target), ".app") {
		return fmt.Errorf("invalid macOS application bundle %q", target)
	}
	if err := exec.Command("pgrep", "-x", "BrownDust II").Run(); err == nil {
		return errGameAlreadyRunning
	}
	args := append([]string{target, "--args"}, gameLaunchArguments()...)
	return exec.Command("open", args...).Start()
}
