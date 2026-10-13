package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	clientconfig "bd2server/internal/client/config"
	"bd2server/internal/client/download"
	clientsetup "bd2server/internal/client/setup"
	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/common/taskgraph"
	"bd2server/internal/common/versionconfig"
)

func prepareClient(args []string) error {
	fs := flag.NewFlagSet("bd2client prepare", flag.ContinueOnError)
	gameDir := fs.String("game-dir", "", "game installation directory")
	origin := fs.String("server-origin", "", "game server HTTPS origin")
	resources := fs.String("resource-dir", "", "directory containing ServerData and GameData")
	downloadOnly := fs.Bool("download", false, "only download and extract the game and resources")
	baseOnly := fs.Bool("base", false, "only initialize BepInEx, plugins, connection settings and launcher preferences")
	packageDir := fs.String("package-dir", "", "client package with plugins, BepInEx ZIP and versions; defaults beside bd2client")
	versionPath := fs.String("version-config", "", "versions.json override")
	manifest := fs.String("manifest", "", "download metadata; defaults to <package-dir>/client-download.json")
	workers := fs.Int("workers", 8, "parallel resource downloads (1-16)")
	probeOnly := fs.Bool("probe-only", false, "measure download origins without changing the installation")
	planOnly := fs.Bool("plan", false, "show ordered dependencies without running any stage")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *workers < 1 || *workers > 16 {
		return errors.New("prepare accepts named options and workers must be 1-16")
	}
	if *downloadOnly && *baseOnly {
		return errors.New("--download and --base select separate stages; omit both to run all stages")
	}
	if *baseOnly && *probeOnly {
		return errors.New("--probe-only cannot be used with --base")
	}
	if *packageDir == "" {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		*packageDir = filepath.Dir(executable)
	}
	var err error
	*packageDir, err = filepath.Abs(*packageDir)
	if err != nil {
		return err
	}
	if *versionPath == "" {
		*versionPath = filepath.Join(*packageDir, versionconfig.FileName)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var versions, downloaded versionconfig.Config
	var settings clientconfig.Settings
	graph, err := taskgraph.New(
		taskgraph.Task{Name: "download", Run: func(ctx context.Context) error {
			installed, err := download.Client(ctx, download.Options{
				PackageDir: *packageDir, Manifest: *manifest,
				GameDir: *gameDir, ResourceDir: *resources, ServerOrigin: *origin,
				Workers: *workers, ProbeOnly: *probeOnly,
			}, os.Stdout)
			if err != nil {
				return err
			}
			*gameDir, *resources, *origin = installed.GameDir, installed.ResourceDir, installed.ServerOrigin
			downloaded = installed.Versions
			return nil
		}},
		taskgraph.Task{Name: "base:configuration", Run: func(context.Context) error {
			if *gameDir == "" {
				*gameDir = filepath.Join(*packageDir, "game")
			}
			if *resources == "" {
				*resources = filepath.Join(*packageDir, "resources")
			}
			if *origin == "" {
				manifestPath := *manifest
				if manifestPath == "" {
					manifestPath = filepath.Join(*packageDir, "client-download.json")
				}
				defaults, err := commonconfig.Load(manifestPath, struct {
					ServerOrigin string `json:"server_origin"`
				}{}, commonconfig.DecodeOptions{AllowBOM: true, RejectTrailing: true})
				if err != nil {
					return err
				}
				*origin = defaults.ServerOrigin
			}
			var err error
			versions, err = versionconfig.Load(*versionPath)
			if err != nil {
				return err
			}
			if !*baseOnly && (versions.GameVersion != downloaded.GameVersion || versions.GameDataVersion != downloaded.GameDataVersion || versions.BundleVersion != downloaded.BundleVersion) {
				return errors.New("client package and download manifest versions differ")
			}
			return nil
		}},
		taskgraph.Task{Name: "base:settings", DependsOn: []string{"base:configuration"}, Run: func(context.Context) error {
			var err error
			settings, err = clientconfig.Normalize(clientconfig.Settings{ServerOrigin: *origin, CDNMode: clientconfig.CDNLocal, LocalResourceDirectory: *resources})
			return err
		}},
		taskgraph.Task{Name: "base:resources", DependsOn: []string{"base:settings"}, Run: func(ctx context.Context) error {
			policyContext, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			_, err := clientsetup.FetchResourcePolicy(policyContext, nil, settings, versions)
			return err
		}},
		taskgraph.Task{Name: "base:bepinex", DependsOn: []string{"base:resources"}, Run: func(context.Context) error {
			return download.InstallBepInEx(*packageDir, *gameDir)
		}},
		taskgraph.Task{Name: "base:patch", DependsOn: []string{"base:bepinex"}, Run: func(context.Context) error {
			_, err := clientsetup.Patch(*gameDir, settings, versions)
			return err
		}},
		taskgraph.Task{Name: "base:plugins", DependsOn: []string{"base:patch"}, Run: func(context.Context) error {
			plugins := filepath.Join(*packageDir, "plugins")
			_, err := clientsetup.InstallPlugins(*gameDir, settings, versions,
				filepath.Join(plugins, "BD2LocalIdentity.dll"), filepath.Join(plugins, "BD2LoginUI.dll"),
				filepath.Join(plugins, "BD2CashShop.dll"), filepath.Join(plugins, "BD2FChat.dll"))
			return err
		}},
		taskgraph.Task{Name: "base:preferences", DependsOn: []string{"base:plugins"}, Run: func(context.Context) error {
			return clientconfig.SavePreferences(*gameDir)
		}},
		taskgraph.Task{Name: "base", DependsOn: []string{"base:preferences"}, Run: func(context.Context) error {
			fmt.Println("Client prepared:", versions.GameVersion)
			fmt.Println("Server:", settings.ServerOrigin)
			return nil
		}},
		taskgraph.Task{Name: "prepare", DependsOn: []string{"download", "base"}},
	)
	if err != nil {
		return err
	}
	target := "prepare"
	if *downloadOnly || *probeOnly {
		target = "download"
	} else if *baseOnly {
		target = "base"
	}
	if *planOnly {
		plan, err := graph.Plan(target)
		if err != nil {
			return err
		}
		fmt.Println(strings.Join(plan, " -> "))
		return nil
	}
	return graph.Run(ctx, target)
}
