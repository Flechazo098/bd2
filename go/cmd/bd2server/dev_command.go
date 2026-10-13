//go:build !release

package main

import (
	"bd2server/internal/common/taskgraph"
	"bd2server/internal/server/app"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
)

func runDevelopmentCommand(args []string) (bool, error) {
	if len(args) == 0 || args[0] != "--dev" {
		return false, nil
	}
	if len(args) < 2 || args[1] != "run" {
		return true, errors.New("usage: bd2server --dev run [serve options]")
	}
	planOnly := false
	serveArgs := make([]string, 0, len(args)-2)
	for _, arg := range args[2:] {
		if arg == "--plan" {
			planOnly = true
		} else {
			serveArgs = append(serveArgs, arg)
		}
	}
	graph, err := taskgraph.New(
		taskgraph.Task{Name: "server:configuration", Run: func(context.Context) error {
			root, err := findDevelopmentRoot()
			if err != nil {
				return err
			}
			serveArgs = appendDefaultFlag(serveArgs, "--version-config", filepath.Join(root, "versions.json"))
			serveArgs = appendDefaultFlag(serveArgs, "--authentication-config", filepath.Join(root, ".build", "config", "authentication.json"))
			serveArgs = appendDefaultFlag(serveArgs, "--resource-config", filepath.Join(root, ".build", "config", "resources.json"))
			serveArgs = appendDefaultFlag(serveArgs, "--game-config", filepath.Join(root, ".build", "config", "game.json"))
			serveArgs = appendDefaultFlag(serveArgs, "--data-dir", filepath.Join(root, "data"))
			serveArgs = appendDefaultFlag(serveArgs, "--state-dir", filepath.Join(root, "data", "state"))
			return nil
		}},
		taskgraph.Task{Name: "runServer", DependsOn: []string{"server:configuration"}, Run: func(context.Context) error { return app.Serve(serveArgs) }},
	)
	if err != nil {
		return true, err
	}
	if planOnly {
		plan, err := graph.Plan("runServer")
		if err != nil {
			return true, err
		}
		fmt.Println(strings.Join(plan, " -> "))
		return true, nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return true, graph.Run(ctx, "runServer")
}

func findDevelopmentRoot() (string, error) {
	working, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve development working directory: %w", err)
	}
	for directory := filepath.Clean(working); ; directory = filepath.Dir(directory) {
		if regularDevelopmentFile(filepath.Join(directory, "versions.json")) &&
			regularDevelopmentFile(filepath.Join(directory, "go", "go.mod")) {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
	}
	return "", errors.New("development repository root not found; run from the bd2 repository")
}

func regularDevelopmentFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func appendDefaultFlag(args []string, name, value string) []string {
	for index, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return args
		}
		if index > 0 && args[index-1] == name {
			return args
		}
	}
	return append(args, name, value)
}

func developmentUsage() string {
	return "\n\nDevelopment build only:\n\tbd2server --dev run [--plan] [serve options]"
}
