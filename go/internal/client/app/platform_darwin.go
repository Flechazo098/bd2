//go:build darwin

package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	clientupdate "bd2server/internal/client/update"
	"bd2server/internal/common/versionconfig"
)

func ShowFatalError(err error) {
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "BD2 Launcher:", err)
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
	args := gameOpenArguments(target, clientupdate.LaunchEnvironment(os.Environ(), target, versionconfig.Current().ClientVersion), proxyURL)
	return exec.Command("open", args...).Start()
}

// LaunchServices needs explicit empty values when the player selects direct.
func gameOpenArguments(target string, environment []string, proxyURL string) []string {
	args := []string{target}
	for _, entry := range gameProxyEnvironment(environment, proxyURL) {
		key, _, found := strings.Cut(entry, "=")
		if found && (isGameProxyEnvironmentKey(key) || strings.HasPrefix(key, "BD2_")) {
			args = append(args, "--env", entry)
		}
	}
	args = append(args, "--args")
	return append(args, gameLaunchArguments()...)
}
