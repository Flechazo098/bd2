//go:build !release

package main

import (
	"errors"
	"fmt"
	"os"
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
	root, err := findDevelopmentRoot()
	if err != nil {
		return true, err
	}
	serveArgs := append([]string(nil), args[2:]...)
	serveArgs = appendDefaultFlag(serveArgs, "--version-config", filepath.Join(root, "versions.json"))
	serveArgs = appendDefaultFlag(serveArgs, "--authentication-config", filepath.Join(root, "authentication.json"))
	serveArgs = appendDefaultFlag(serveArgs, "--resource-config", filepath.Join(root, "resources.json"))
	serveArgs = appendDefaultFlag(serveArgs, "--game-config", filepath.Join(root, "game.json"))
	serveArgs = appendDefaultFlag(serveArgs, "--data-dir", filepath.Join(root, "data"))
	serveArgs = appendDefaultFlag(serveArgs, "--state", filepath.Join(root, "data", "state", "state.db"))
	return true, serve(serveArgs)
}

func findDevelopmentRoot() (string, error) {
	working, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve development working directory: %w", err)
	}
	for directory := filepath.Clean(working); ; directory = filepath.Dir(directory) {
		if regularDevelopmentFile(filepath.Join(directory, "versions.json")) &&
			regularDevelopmentFile(filepath.Join(directory, "go", "go.mod")) &&
			regularDevelopmentFile(filepath.Join(directory, "authentication.json")) &&
			regularDevelopmentFile(filepath.Join(directory, "resources.json")) {
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
	return "\n\nDevelopment build only:\n\tbd2server --dev run [serve options]"
}
