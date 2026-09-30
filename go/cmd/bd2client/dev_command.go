//go:build !release

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	clientconfig "bd2server/internal/client/config"
	clientlayout "bd2server/internal/client/layout"
)

func developmentRunOptions(args []string) ([]string, clientRunOptions, error) {
	if len(args) == 0 || args[0] != "--dev" {
		return args, clientRunOptions{}, nil
	}
	if len(args) < 2 || args[1] != "run" {
		return nil, clientRunOptions{}, errors.New("usage: bd2client --dev run [client options]")
	}
	root, err := findClientDevelopmentRoot()
	if err != nil {
		return nil, clientRunOptions{}, err
	}
	clientArgs := append([]string(nil), args[2:]...)
	gameDir, err := clientDevelopmentGameDirectory(clientArgs)
	if err != nil {
		return nil, clientRunOptions{}, err
	}
	if gameDir == "" {
		preferences, preferenceErr := clientconfig.LoadPreferences()
		if preferenceErr == nil {
			gameDir = preferences.GameDirectory
		}
	}
	if gameDir != "" {
		if err := buildDevelopmentPlugins(root, gameDir); err != nil {
			return nil, clientRunOptions{}, err
		}
	}
	return clientArgs, clientRunOptions{
		versionConfigPath:   filepath.Join(root, "versions.json"),
		logExecutablePath:   filepath.Join(root, "data", "bd2client-dev"),
		localIdentityPlugin: filepath.Join(root, "plugins", "LocalIdentity", "bin", "Release", "netstandard2.1", "BD2LocalIdentity.dll"),
		loginUIPlugin:       filepath.Join(root, "plugins", "LoginUI", "bin", "Release", "netstandard2.1", "BD2LoginUI.dll"),
	}, nil
}

func findClientDevelopmentRoot() (string, error) {
	working, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve development working directory: %w", err)
	}
	for directory := filepath.Clean(working); ; directory = filepath.Dir(directory) {
		if clientDevelopmentFile(filepath.Join(directory, "versions.json")) &&
			clientDevelopmentFile(filepath.Join(directory, "go", "go.mod")) &&
			clientDevelopmentFile(filepath.Join(directory, "plugins", "LocalIdentity", "LocalIdentity.csproj")) &&
			clientDevelopmentFile(filepath.Join(directory, "plugins", "LoginUI", "LoginUI.csproj")) {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
	}
	return "", errors.New("development repository root not found; run from the bd2 repository")
}

func clientDevelopmentFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func clientDevelopmentGameDirectory(args []string) (string, error) {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--game-dir" {
			if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
				return "", errors.New("--game-dir requires a directory")
			}
			return filepath.Clean(args[index+1]), nil
		}
		if strings.HasPrefix(arg, "--game-dir=") {
			value := strings.TrimSpace(strings.TrimPrefix(arg, "--game-dir="))
			if value == "" {
				return "", errors.New("--game-dir requires a directory")
			}
			return filepath.Clean(value), nil
		}
	}
	return "", nil
}

func buildDevelopmentPlugins(root, gameDir string) error {
	installation, err := clientlayout.Resolve(gameDir)
	if err != nil {
		return nil
	}
	if !clientDevelopmentFile(filepath.Join(installation.BepInEx, "core", "BepInEx.dll")) {
		return nil
	}
	projects := []string{
		filepath.Join(root, "plugins", "LocalIdentity", "LocalIdentity.csproj"),
		filepath.Join(root, "plugins", "LoginUI", "LoginUI.csproj"),
	}
	for _, project := range projects {
		command := exec.Command(
			"dotnet", "build", project, "-c", "Release",
			"-p:GameDir="+filepath.Clean(gameDir),
			"-p:BD2ManagedDir="+filepath.Join(installation.Data, "Managed"),
			"-p:BD2BepInExDir="+installation.BepInEx,
			"--nologo",
		)
		command.Dir = root
		output, err := command.CombinedOutput()
		if err != nil {
			message := strings.TrimSpace(string(output))
			if message == "" {
				message = err.Error()
			}
			return fmt.Errorf("build development plugin %s: %s", filepath.Base(project), message)
		}
	}
	return nil
}
