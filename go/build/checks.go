package main

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	"bd2server/internal/client/plugin"
	"bd2server/internal/common/taskgraph"
)

func (t task) checkCSharp(projects []string) error {
	allowed := csharpProjects()
	if len(projects) == 0 {
		projects = allowed
	}
	var tasks []taskgraph.Task
	var targets []string
	seen := map[string]bool{}
	for _, project := range projects {
		if !slices.Contains(allowed, project) {
			return fmt.Errorf("unknown C# project %q", project)
		}
		if seen[project] {
			continue
		}
		seen[project] = true
		file := filepath.Join(t.root, "plugins", project, project+".csproj")
		for _, definition := range plugin.Definitions() {
			if definition.Name() == project {
				file = definition.ProjectFile(t.root)
				break
			}
		}
		build, format := "csharp:build:"+project, "csharp:style:"+project
		tasks = append(tasks,
			taskgraph.Task{Name: build, Run: func(context.Context) error {
				_, err := t.sdkCommand(t.root, nil, false, "build", file, "-c", "Release", "-t:Rebuild", "--nologo", "-warnaserror")
				return err
			}},
			taskgraph.Task{Name: format, DependsOn: []string{build}, Run: func(context.Context) error {
				_, err := t.sdkCommand(t.root, map[string]string{"Configuration": "Release"}, false, "format", "style", file, "--verify-no-changes", "--severity", "info", "--no-restore")
				return err
			}})
		targets = append(targets, format)
	}
	tasks = append(tasks, taskgraph.Task{Name: "check-csharp", DependsOn: targets})
	return t.execute("check-csharp", tasks...)
}

func csharpProjects() []string {
	projects := []string{"GameSdk", "CaptureEnvironment"}
	for _, d := range plugin.Definitions() {
		projects = append(projects, d.Name())
	}
	return projects
}
