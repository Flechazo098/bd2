//go:build !release

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"bd2server/internal/client/iconresource"
	clientlayout "bd2server/internal/client/layout"
	clientplugin "bd2server/internal/client/plugin"
	"bd2server/internal/common/taskgraph"
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
	if len(args) == 0 || args[0] != "--dev" || slices.Contains(args, "--plan") {
		return false, nil
	}
	if len(args) < 2 || args[1] != "run" {
		return false, nil
	}
	root, err := findClientDevelopmentRoot()
	if err != nil {
		return false, err
	}
	var changed, relaunched bool
	graph, err := taskgraph.New(
		taskgraph.Task{Name: "client:icon", Run: func(context.Context) error {
			var err error
			changed, err = iconresource.Generate(root, runtime.GOOS, runtime.GOARCH)
			return err
		}},
		taskgraph.Task{Name: "client:backend", DependsOn: []string{"client:icon"}, Run: func(context.Context) error {
			if wailsDevelopmentBuild && !changed {
				return nil
			}
			childArgs := []string{"run", "-tags", "production", "./cmd/bd2client", "--dev", "run"}
			childArgs = append(childArgs, args[2:]...)
			if err := runDevelopmentChild(root, childArgs); err != nil {
				return fmt.Errorf("run Wails development client: %w", err)
			}
			relaunched = true
			return nil
		}},
	)
	if err != nil {
		return false, err
	}
	err = graph.Run(context.Background(), "client:backend")
	return relaunched, err
}

func runClientDevelopmentCommand(args []string) (bool, error) {
	if len(args) == 0 || args[0] != "--dev" {
		return false, nil
	}
	if len(args) < 2 || args[1] != "run" {
		return true, errors.New("usage: bd2client --dev run [--plan] [client options]")
	}
	planOnly := false
	clientArgs := make([]string, 0, len(args)-2)
	for _, arg := range args[2:] {
		if arg == "--plan" {
			planOnly = true
		} else {
			clientArgs = append(clientArgs, arg)
		}
	}
	var root, gameDir string
	var options clientRunOptions
	graph, err := taskgraph.New(
		taskgraph.Task{Name: "client:configuration", Run: func(context.Context) error {
			var err error
			root, err = findClientDevelopmentRoot()
			if err != nil {
				return err
			}
			gameDir, err = clientDevelopmentGameDirectory(clientArgs)
			if err != nil {
				return err
			}
			if gameDir == "" {
				config, err := loadClientDevelopmentConfig(filepath.Join(root, "go", "config.json"))
				if err != nil {
					return err
				}
				gameDir = config.GameDirectory
			}
			return nil
		}},
		taskgraph.Task{Name: "client:plugins", DependsOn: []string{"client:configuration"}, Run: func(ctx context.Context) error {
			if gameDir == "" {
				return nil
			}
			return buildDevelopmentPlugins(ctx, root, gameDir)
		}},
		taskgraph.Task{Name: "client:options", DependsOn: []string{"client:plugins"}, Run: func(context.Context) error {
			options = clientRunOptions{
				versionConfigPath: filepath.Join(root, "versions.json"),
				logExecutablePath: filepath.Join(root, "data", "bd2client-dev"),
				plugins:           clientplugin.Development(root),
			}
			return nil
		}},
		taskgraph.Task{Name: "runClient", DependsOn: []string{"client:options"}, Run: func(context.Context) error { return runClient(clientArgs, options) }},
	)
	if err != nil {
		return true, err
	}
	if planOnly {
		plan, err := graph.Plan("runClient")
		if err != nil {
			return true, err
		}
		fmt.Println(strings.Join(plan, " -> "))
		return true, nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return true, graph.Run(ctx, "runClient")
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
			clientDevelopmentFile(filepath.Join(directory, "go", "go.mod")) {
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
	for index, arg := range args {
		if arg == "--game-dir" {
			if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
				return "", errors.New("--game-dir requires a directory")
			}
			return filepath.Clean(args[index+1]), nil
		}
		if value, ok := strings.CutPrefix(arg, "--game-dir="); ok {
			value = strings.TrimSpace(value)
			if value == "" {
				return "", errors.New("--game-dir requires a directory")
			}
			return filepath.Clean(value), nil
		}
	}
	return "", nil
}

func buildDevelopmentPlugins(ctx context.Context, root, gameDir string) error {
	installation, err := clientlayout.Resolve(gameDir)
	if err != nil {
		return nil
	}
	if !clientDevelopmentFile(filepath.Join(installation.BepInEx, "core", "BepInEx.dll")) {
		return nil
	}
	fmt.Fprintln(os.Stderr, "bd2client: preparing development plugins; cached inputs will be reused")
	return clientplugin.BuildDevelopment(ctx, root, gameDir, os.Stderr)
}
