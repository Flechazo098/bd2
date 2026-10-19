package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	clientconfig "bd2server/internal/client/config"
	clientinstance "bd2server/internal/client/instance"
	clientplugin "bd2server/internal/client/plugin"
	clientsetup "bd2server/internal/client/setup"
	"bd2server/internal/common/taskgraph"
	"bd2server/internal/common/versionconfig"
)

func (t task) runClient2(args []string) error {
	fs := flag.NewFlagSet("runClient2", flag.ContinueOnError)
	name := fs.String("instance", "player2", "independent local game instance name")
	gameDir := fs.String("game-dir", "", "original game installation; defaults to go/config.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	var game, launcher string
	var versions versionconfig.Config
	var settings clientconfig.Settings
	var instance clientinstance.Instance
	return t.execute("runClient2",
		taskgraph.Task{Name: "instance:configuration", Run: func(context.Context) error {
			var err error
			game, _, _, err = t.gameDirectory(*gameDir)
			if err != nil {
				return err
			}
			versions, err = versionconfig.Load(filepath.Join(t.root, "versions.json"))
			if err != nil {
				return err
			}
			if _, err := clientsetup.Inspect(game, versions); err != nil {
				return err
			}
			settings, err = clientconfig.Load(game)
			if err != nil {
				return fmt.Errorf("save the original client's connection settings first: %w", err)
			}
			settings.ProxyURL = ""
			return nil
		}},
		taskgraph.Task{Name: "instance:prepare", DependsOn: []string{"instance:configuration"}, Run: func(context.Context) error {
			var err error
			instance, err = clientinstance.Prepare(*name, game, versions.GameVersion)
			if err != nil {
				return err
			}
			if !instance.Created {
				settings, err = clientconfig.Load(instance.GameDirectory)
				if err != nil {
					return fmt.Errorf("load second client's connection settings: %w", err)
				}
			}
			if _, err := clientsetup.SaveSettings(instance.GameDirectory, settings); err != nil {
				return fmt.Errorf("prepare second client's resources: %w", err)
			}
			if settings.CDNMode == clientconfig.CDNLocal {
				if err := clientsetup.InstallLocalCatalog(instance.GameDirectory, settings.LocalResourceDirectory, versions.BundleVersion); err != nil {
					return fmt.Errorf("prepare second client's catalog: %w", err)
				}
			}
			return nil
		}},
		taskgraph.Task{Name: "instance:plugins", DependsOn: []string{"instance:prepare"}, Run: func(ctx context.Context) error {
			if !instance.Created {
				return nil
			}
			if err := clientplugin.BuildDevelopment(ctx, t.root, game, os.Stdout); err != nil {
				return err
			}
			_, err := clientsetup.InstallPlugins(instance.GameDirectory, clientplugin.Development(t.root))
			return err
		}},
		taskgraph.Task{Name: "instance:launcher", DependsOn: []string{"instance:plugins"}, Run: func(context.Context) error {
			var err error
			launcher, err = instance.WriteLauncher()
			if err != nil {
				return err
			}
			fmt.Println("Second client ready:", launcher)
			fmt.Println("Unity log:", filepath.Join(instance.Root, "logs", "Player.log"))
			fmt.Println("BepInEx log:", filepath.Join(instance.GameDirectory, "BepInEx", "LogOutput.log"))
			return nil
		}},
		taskgraph.Task{Name: "runClient2", DependsOn: []string{"instance:launcher"}, Run: func(context.Context) error {
			if t.target.goos == "windows" {
				return exec.CommandContext(t.ctx, "wscript.exe", "//nologo", launcher).Run()
			}
			command := exec.Command("/bin/sh", launcher)
			if err := command.Start(); err != nil {
				return err
			}
			return command.Process.Release()
		}},
	)
}
