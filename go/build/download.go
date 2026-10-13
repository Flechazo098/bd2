package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"bd2server/internal/common/taskgraph"
)

func (t task) runDownload(name string, args []string) error {
	if name == "download-resources" {
		forwarded, _, err := absoluteDownloadPaths(args, "output")
		if err != nil {
			return err
		}
		return t.execute(name, taskgraph.Task{Name: name, Run: func(context.Context) error {
			return t.command("go", append([]string{"run", "-tags", "production", "./cmd/bd2client", name}, forwarded...)...)
		}})
	}
	forwarded, err := downloadClientArguments(t.root, args)
	if err != nil {
		return err
	}
	if t.planOnly {
		return t.command("go", append([]string{"run", "-tags", "production", "./cmd/bd2client", "prepare", "--plan"}, forwarded...)...)
	}
	return t.execute(name, taskgraph.Task{Name: name, Run: func(context.Context) error {
		return t.command("go", append([]string{"run", "-tags", "production", "./cmd/bd2client", "prepare"}, forwarded...)...)
	}})
}

func downloadClientArguments(root string, args []string) ([]string, error) {
	forwarded, paths, err := absoluteDownloadPaths(args, "package-dir", "manifest", "version-config", "game-dir", "resource-dir")
	if err != nil {
		return nil, err
	}
	if paths["package-dir"] == "" {
		forwarded = append(forwarded, "--package-dir", root)
		if paths["manifest"] == "" {
			forwarded = append(forwarded, "--manifest", filepath.Join(root, "tools", "client", "client-download.json"))
		}
	} else if paths["manifest"] == "" {
		forwarded = append(forwarded, "--manifest", filepath.Join(paths["package-dir"], "client-download.json"))
	}
	return forwarded, nil
}

func absoluteDownloadPaths(args []string, names ...string) ([]string, map[string]string, error) {
	forwarded := append([]string{}, args...)
	paths := map[string]string{}
	for i := 0; i < len(forwarded); i++ {
		if !strings.HasPrefix(forwarded[i], "-") {
			continue
		}
		name, value, assigned := strings.Cut(forwarded[i], "=")
		name = strings.TrimLeft(name, "-")
		pathOption := false
		for _, candidate := range names {
			pathOption = pathOption || name == candidate
		}
		if !pathOption {
			continue
		}
		if !assigned {
			i++
			if i >= len(forwarded) {
				return nil, nil, fmt.Errorf("--%s requires a path", name)
			}
			value = forwarded[i]
		}
		if value == "" {
			return nil, nil, fmt.Errorf("--%s requires a path", name)
		}
		absolute, err := filepath.Abs(value)
		if err != nil {
			return nil, nil, err
		}
		paths[name] = absolute
		if assigned {
			forwarded[i] = "--" + name + "=" + absolute
		} else {
			forwarded[i] = absolute
		}
	}
	return forwarded, paths, nil
}
