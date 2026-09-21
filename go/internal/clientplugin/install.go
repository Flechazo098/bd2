package clientplugin

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	FileName           = "BD2LocalIdentity.dll"
	BepInExReleasesURL = "https://github.com/BepInEx/BepInEx/releases"
)

type Result struct {
	Destination string
	Changed     bool
}

func ResolvePackaged(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Clean(explicit), nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("clientplugin: resolve server executable: %w", err)
	}
	return filepath.Join(filepath.Dir(executable), "plugins", FileName), nil
}

// Install verifies that the user installed BepInEx, then atomically stages the
// packaged local-identity plugin into its plugins directory. It never installs
// or downloads BepInEx itself.
func Install(gameDir, source string) (Result, error) {
	if gameDir == "" || source == "" {
		return Result{}, errors.New("clientplugin: game directory and plugin source are required")
	}
	gameDir = filepath.Clean(gameDir)
	source = filepath.Clean(source)
	gameExecutable := filepath.Join(gameDir, "BrownDust II.exe")
	if info, err := os.Stat(gameExecutable); err != nil || info.IsDir() {
		return Result{}, fmt.Errorf("clientplugin: game executable is unavailable at %q", gameExecutable)
	}
	bepInEx := filepath.Join(gameDir, "BepInEx", "core", "BepInEx.dll")
	if info, err := os.Stat(bepInEx); err != nil || info.IsDir() {
		return Result{}, fmt.Errorf("clientplugin: BepInEx is not installed; install it manually from %s, then restart the server; %s was not copied", BepInExReleasesURL, FileName)
	}
	sourceData, err := os.ReadFile(source)
	if err != nil {
		return Result{}, fmt.Errorf("clientplugin: read packaged %s: %w", FileName, err)
	}
	if len(sourceData) == 0 {
		return Result{}, fmt.Errorf("clientplugin: packaged %s is empty", FileName)
	}
	pluginDir := filepath.Join(gameDir, "BepInEx", "plugins")
	destination := filepath.Join(pluginDir, FileName)
	if installed, err := os.ReadFile(destination); err == nil {
		if bytes.Equal(hash(installed), hash(sourceData)) {
			return Result{Destination: destination}, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("clientplugin: inspect installed plugin: %w", err)
	}
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("clientplugin: create plugin directory: %w", err)
	}
	temporary, err := os.CreateTemp(pluginDir, ".BD2LocalIdentity-*.tmp")
	if err != nil {
		return Result{}, fmt.Errorf("clientplugin: create temporary plugin: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err = io.Copy(temporary, bytes.NewReader(sourceData)); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Result{}, fmt.Errorf("clientplugin: stage plugin: %w", err)
	}
	if err := replaceFile(temporaryPath, destination); err != nil {
		return Result{}, fmt.Errorf("clientplugin: install plugin (close the game client first): %w", err)
	}
	return Result{Destination: destination, Changed: true}, nil
}

func hash(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}
