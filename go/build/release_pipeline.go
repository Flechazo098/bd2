package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	clientplugin "bd2server/internal/client/plugin"
	"bd2server/internal/common/taskgraph"
)

type release struct {
	task                                                         task
	options                                                      buildOptions
	versions                                                     releaseVersions
	schedules                                                    []string
	scheduleName, game, bepinex                                  string
	buildRoot, packageRoot, server, client, serverZip, clientZip string
}

func (t task) build(opts buildOptions) error {
	r := &release{task: t, options: opts, buildRoot: filepath.Join(t.root, ".build")}
	nodes := []taskgraph.Task{
		{Name: "release:metadata", Run: func(context.Context) error { return r.loadMetadata() }},
		{Name: "release:schedules", DependsOn: []string{"release:metadata"}, Run: func(context.Context) error { return r.exportSchedules() }},
	}
	if opts.schedulesOnly {
		return t.execute("release:schedules", nodes...)
	}
	checks := []string{"release:metadata"}
	if !opts.skipTests {
		var prior string
		for _, check := range []struct {
			name string
			args []string
		}{
			{"go:test", []string{"test", "./..."}}, {"go:vet", []string{"vet", "./..."}},
			{"go:test-release", []string{"test", "-tags", "release,production", "./..."}}, {"go:vet-release", []string{"vet", "-tags", "release,production", "./..."}},
		} {
			dependencies := []string{"release:metadata"}
			if prior != "" {
				dependencies = []string{prior}
			}
			nodes = append(nodes, taskgraph.Task{Name: check.name, DependsOn: dependencies, Run: func(context.Context) error { return t.command("go", check.args...) }})
			prior = check.name
		}
		checks = append(checks, prior)
	}
	nodes = append(nodes,
		t.clientIcon(),
		taskgraph.Task{Name: "release:stage", DependsOn: checks, Run: func(context.Context) error { return r.prepareStage() }},
		taskgraph.Task{Name: "release:server", DependsOn: []string{"release:stage"}, Run: func(context.Context) error { return r.compileServer() }},
		taskgraph.Task{Name: "release:client", DependsOn: []string{"release:stage", "client:icon"}, Run: func(context.Context) error { return r.compileClient() }},
		taskgraph.Task{Name: "release:plugins", DependsOn: []string{"release:stage"}, Run: func(context.Context) error { return clientplugin.BuildDevelopment(t.ctx, t.root, r.game, os.Stdout) }},
		taskgraph.Task{Name: "release:assets", DependsOn: []string{"release:stage", "release:plugins"}, Run: func(context.Context) error { return r.stageAssets() }},
		taskgraph.Task{Name: "build", DependsOn: []string{"release:server", "release:client", "release:assets"}, Run: func(context.Context) error { return r.archive() }},
	)
	return t.execute("build", nodes...)
}

func (r *release) loadMetadata() error {
	var err error
	r.versions, err = r.task.versions()
	if err != nil {
		return err
	}
	r.schedules, r.scheduleName, err = r.task.schedules(r.versions.Game)
	if err != nil {
		return err
	}
	if r.options.schedulesOnly {
		return nil
	}
	r.game, _, r.bepinex, err = r.task.gameDirectory(r.options.gameDir)
	if err != nil {
		return err
	}
	r.packageRoot = filepath.Join(r.buildRoot, "package")
	r.server, r.client = filepath.Join(r.packageRoot, "bd2server"), filepath.Join(r.packageRoot, "bd2client")
	suffix := "-" + r.task.target.platform + "-" + r.task.target.architecture + ".zip"
	r.serverZip = filepath.Join(r.buildRoot, "bd2server-"+r.versions.Server+suffix)
	r.clientZip = filepath.Join(r.buildRoot, "bd2client-"+r.versions.Client+suffix)
	return nil
}

func (r *release) exportSchedules() error {
	out := filepath.Join(r.buildRoot, r.scheduleName)
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	for _, file := range r.schedules {
		if err := copyFile(file, filepath.Join(out, filepath.Base(file))); err != nil {
			return err
		}
	}
	fmt.Println("Built schedule release:", out)
	return nil
}

func (r *release) prepareStage() error {
	if err := os.MkdirAll(r.buildRoot, 0755); err != nil {
		return err
	}
	for _, path := range []string{r.packageRoot, r.serverZip, r.clientZip} {
		if err := removeBuildOutput(r.buildRoot, path); err != nil {
			return err
		}
	}
	for _, path := range []string{filepath.Join(r.server, "go"), filepath.Join(r.server, "data", "state"), filepath.Join(r.server, "schedules"), filepath.Join(r.client, "plugins")} {
		if err := os.MkdirAll(path, 0755); err != nil {
			return err
		}
	}
	return nil
}

func (r *release) executable(name string) string {
	if r.task.target.goos == "windows" {
		return name + ".exe"
	}
	return name
}

func (r *release) compileServer() error {
	return r.task.command("go", "build", "-tags", "release", "-trimpath", "-ldflags", "-s -w", "-o", filepath.Join(r.server, r.executable("bd2server")), "./cmd/bd2server")
}

func (r *release) compileClient() error {
	flags := "-s -w"
	if r.task.target.goos == "windows" {
		flags = "-H windowsgui -s -w"
	}
	return r.task.command("go", "build", "-tags", "release,production", "-trimpath", "-ldflags", flags, "-o", filepath.Join(r.client, r.executable("bd2client")), "./cmd/bd2client")
}

func (r *release) stageAssets() error {
	root := r.task.root
	if err := copyFile(filepath.Join(root, "tools", "client", "client-download.json"), filepath.Join(r.client, "client-download.json")); err != nil {
		return err
	}
	if r.task.target.goos == "windows" {
		if err := r.task.packageBepInEx(r.game, r.bepinex, r.client); err != nil {
			return err
		}
	}
	var shared []byte
	for _, plugin := range []string{"LocalIdentity", "LoginUI", "CashShop", "FChat"} {
		out := filepath.Join(root, "plugins", plugin, "bin", "Release", "netstandard2.1")
		raw, err := os.ReadFile(filepath.Join(out, "BD2.GameNames.dll"))
		if err != nil {
			return err
		}
		if shared != nil && !bytes.Equal(shared, raw) {
			return errors.New("plugins were built with different BD2.GameNames libraries")
		}
		shared = raw
		for _, name := range []string{"BD2" + plugin + ".dll", "BD2.GameNames.dll"} {
			if err := copyFile(filepath.Join(out, name), filepath.Join(r.client, "plugins", name)); err != nil {
				return err
			}
		}
	}
	if err := copyTree(filepath.Join(root, "go", "seed"), filepath.Join(r.server, "go", "seed")); err != nil {
		return err
	}
	for _, file := range r.schedules {
		if err := copyFile(file, filepath.Join(r.server, "schedules", filepath.Base(file))); err != nil {
			return err
		}
	}
	for _, name := range []string{"versions.json", "README.md", "LICENSE"} {
		for _, out := range []string{r.server, r.client} {
			if err := copyFile(filepath.Join(root, name), filepath.Join(out, name)); err != nil {
				return err
			}
		}
	}
	for _, name := range []string{"AUTHENTICATION.md", "RESOURCES.md", "GAME_CONFIGURATION.md"} {
		if err := copyFile(filepath.Join(root, name), filepath.Join(r.server, name)); err != nil {
			return err
		}
	}
	return nil
}

func (r *release) archive() error {
	if err := archiveDirectory(r.server, r.serverZip); err != nil {
		return err
	}
	if err := archiveDirectory(r.client, r.clientZip); err != nil {
		return err
	}
	fmt.Println("Built server archive:", r.serverZip)
	fmt.Println("Built client archive:", r.clientZip)
	return nil
}
