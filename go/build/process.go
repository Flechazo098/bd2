package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"bd2server/internal/common/taskgraph"
)

type task struct {
	root     string
	target   target
	ctx      context.Context
	planOnly bool
}

func (t task) execute(target string, tasks ...taskgraph.Task) error {
	graph, err := taskgraph.New(tasks...)
	if err != nil {
		return err
	}
	if t.planOnly {
		plan, err := graph.Plan(target)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, strings.Join(plan, "\n"))
		return err
	}
	return graph.Run(t.ctx, target)
}

func repositoryRoot() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	for dir := filepath.Dir(exe); ; dir = filepath.Dir(dir) {
		if regular(filepath.Join(dir, "versions.json")) && regular(filepath.Join(dir, "go", "go.mod")) {
			return dir, nil
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return "", errors.New("repository root not found beside the cached build tool; launch through bd2w")
}

// Every child receives a process-local native target and repository-local cache.
func (t task) command(program string, args ...string) error {
	cmd := exec.CommandContext(t.ctx, program, args...)
	cmd.Dir = filepath.Join(t.root, "go")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = replaceEnvironment(os.Environ(), map[string]string{"GOCACHE": filepath.Join(t.root, "go", ".cache", "go-build"), "GOOS": t.target.goos, "GOARCH": t.target.goarch, "GOARM": t.target.goarm})
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", program, strings.Join(args, " "), err)
	}
	return nil
}

func replaceEnvironment(env []string, values map[string]string) []string {
	out := make([]string, 0, len(env)+len(values))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		remove := false
		for wanted := range values {
			if strings.EqualFold(key, wanted) {
				remove = true
				break
			}
		}
		if !remove {
			out = append(out, entry)
		}
	}
	for key, value := range values {
		if value != "" {
			out = append(out, key+"="+value)
		}
	}
	return out
}
