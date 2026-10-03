//go:build !release

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	clientlayout "bd2server/internal/client/layout"
)

type clientDevelopmentConfig struct {
	SchemaVersion int    `json:"schema_version"`
	GameDirectory string `json:"game_directory"`
}

var runDevelopmentChild = func(root string, args []string) error {
	command := exec.Command("go", args...)
	command.Dir = filepath.Join(root, "go")
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

// relaunchDevelopmentIfNeeded preserves the documented plain `go run`
// command. Wails selects its real desktop backend with the `dev` or
// `production` build tag; without either it deliberately links a diagnostic
// placeholder. The outer process therefore rebuilds itself once with
// `production`, retaining the embedded frontend and normal native host while
// the project-level --dev option supplies development paths and plugin builds.
func relaunchDevelopmentIfNeeded(args []string) (bool, error) {
	if len(args) == 0 || args[0] != "--dev" || wailsDevelopmentBuild {
		return false, nil
	}
	if len(args) < 2 || args[1] != "run" {
		return false, nil
	}
	root, err := findClientDevelopmentRoot()
	if err != nil {
		return false, err
	}
	childArgs := []string{"run", "-tags", "production", "./cmd/bd2client", "--dev", "run"}
	childArgs = append(childArgs, args[2:]...)
	if err := runDevelopmentChild(root, childArgs); err != nil {
		return false, fmt.Errorf("run Wails development client: %w", err)
	}
	return true, nil
}

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
		config, configErr := loadClientDevelopmentConfig(filepath.Join(root, "go", "config.json"))
		if configErr != nil {
			return nil, clientRunOptions{}, configErr
		}
		gameDir = config.GameDirectory
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

func loadClientDevelopmentConfig(path string) (clientDevelopmentConfig, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return clientDevelopmentConfig{}, fmt.Errorf("development config %s is missing; copy go/config.example.json to go/config.json or pass --game-dir", path)
	}
	if err != nil {
		return clientDevelopmentConfig{}, fmt.Errorf("read development config: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var config clientDevelopmentConfig
	if err := decoder.Decode(&config); err != nil {
		return clientDevelopmentConfig{}, fmt.Errorf("decode development config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return clientDevelopmentConfig{}, errors.New("development config must contain exactly one JSON object")
	}
	if config.SchemaVersion != 1 || strings.TrimSpace(config.GameDirectory) == "" {
		return clientDevelopmentConfig{}, errors.New("development config requires schema_version 1 and game_directory")
	}
	absolute, err := filepath.Abs(filepath.Clean(config.GameDirectory))
	if err != nil {
		return clientDevelopmentConfig{}, fmt.Errorf("resolve development game directory: %w", err)
	}
	config.GameDirectory = absolute
	return config, nil
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
		fmt.Fprintf(os.Stderr, "bd2client: preparing development plugin %s; first-time game source generation may take several minutes\n", filepath.Base(project))
		command := exec.Command(
			"dotnet", "build", project, "-c", "Release",
			"-p:GameDir="+filepath.Clean(gameDir),
			"-p:BD2ManagedDir="+filepath.Join(installation.Data, "Managed"),
			"-p:BD2BepInExDir="+installation.BepInEx,
			"--nologo",
		)
		command.Dir = root
		// SDK preparation can decompile the entire game before the window opens.
		// Stream progress immediately, while retaining diagnostics for failures.
		var output bytes.Buffer
		writer := io.MultiWriter(os.Stderr, &output)
		command.Stdout = writer
		command.Stderr = writer
		err := command.Run()
		if err != nil {
			message := strings.TrimSpace(output.String())
			if message == "" {
				message = err.Error()
			}
			return fmt.Errorf("build development plugin %s: %s", filepath.Base(project), message)
		}
	}
	return nil
}
