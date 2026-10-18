package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	clientconfig "bd2server/internal/client/config"
	"bd2server/internal/client/preparation"
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	result, err := preparation.Run(ctx, preparation.Options{
		PackageDirectory: *packageDir, DownloadManifest: *manifest, VersionConfig: *versionPath,
		GameDirectory: *gameDir, ResourceDirectory: *resources,
		Settings: clientconfig.Settings{ServerOrigin: *origin, CDNMode: clientconfig.CDNLocal, LocalResourceDirectory: *resources},
		Workers:  *workers, DownloadOnly: *downloadOnly, BaseOnly: *baseOnly, ProbeOnly: *probeOnly, PlanOnly: *planOnly,
	}, os.Stdout)
	if err != nil {
		return err
	}
	if *planOnly {
		fmt.Println(strings.Join(result.Plan, " -> "))
	} else if !*downloadOnly && !*probeOnly {
		fmt.Println("Client prepared:", result.Versions.GameVersion)
		fmt.Println("Server:", result.Settings.ServerOrigin)
	}
	return nil
}
