package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	clientconfig "bd2server/internal/client/config"
	clientinstance "bd2server/internal/client/instance"
	clientplugin "bd2server/internal/client/plugin"
	clientsetup "bd2server/internal/client/setup"
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
	game, _, _, err := t.gameDirectory(*gameDir)
	if err != nil {
		return err
	}
	versions, err := clientconfig.LoadReleaseVersions(filepath.Join(t.root, "versions.json"))
	if err != nil {
		return err
	}
	if _, err := clientsetup.Inspect(game, versions); err != nil {
		return err
	}
	settings, err := clientconfig.Load(game)
	if err != nil {
		return fmt.Errorf("save the original client's connection settings first: %w", err)
	}
	settings.ProxyURL = ""

	instance, err := clientinstance.Prepare(*name, game, versions.GameVersion)
	if err != nil {
		return err
	}
	if !instance.Created {
		settings, err = clientconfig.Load(instance.GameDirectory)
		if err != nil {
			return fmt.Errorf("load second client's connection settings: %w", err)
		}
	}
	if _, err := clientsetup.SaveSettings(instance.GameDirectory, settings, versions); err != nil {
		return fmt.Errorf("prepare second client's resources: %w", err)
	}
	if instance.Created {
		if err := clientplugin.BuildDevelopment(t.root, game, os.Stdout); err != nil {
			return err
		}
		plugins := func(name string) string {
			return filepath.Join(t.root, "plugins", name, "bin", "Release", "netstandard2.1", "BD2"+name+".dll")
		}
		if _, err := clientsetup.InstallPlugins(instance.GameDirectory, settings, versions, plugins("LocalIdentity"), plugins("LoginUI"), plugins("CashShop"), plugins("FChat")); err != nil {
			return err
		}
	}
	launcher, err := instance.WriteLauncher()
	if err != nil {
		return err
	}
	fmt.Println("Second client ready:", launcher)
	fmt.Println("Unity log:", filepath.Join(instance.Root, "logs", "Player.log"))
	fmt.Println("BepInEx log:", filepath.Join(instance.GameDirectory, "BepInEx", "LogOutput.log"))
	if t.target.goos == "windows" {
		return exec.Command("wscript.exe", "//nologo", launcher).Run()
	}
	command := exec.Command("/bin/sh", launcher)
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
