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
	// LaunchServices does not inherit open's environment; --env explicitly
	// supplies the Unity variables to the newly launched application.
	args := []string{target}
	for _, entry := range gameProxyEnvironment(os.Environ(), systemGameProxy()) {
		key, _, found := strings.Cut(entry, "=")
		if found && (strings.EqualFold(key, "UNITY_PROXYSERVER") || strings.EqualFold(key, "UNITY_NOPROXY")) {
			args = append(args, "--env", entry)
		}
	}
	args = append(args, "--args")
	args = append(args, gameLaunchArguments()...)
	return exec.Command("open", args...).Start()
}
