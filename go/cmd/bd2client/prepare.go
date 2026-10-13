package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	clientconfig "bd2server/internal/client/config"
	clientsetup "bd2server/internal/client/setup"
)

func prepareClient(args []string) error {
	fs := flag.NewFlagSet("bd2client prepare", flag.ContinueOnError)
	gameDir := fs.String("game-dir", "", "game installation directory")
	origin := fs.String("server-origin", "", "game server HTTPS origin")
	resources := fs.String("resource-dir", "", "directory containing ServerData and GameData")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *gameDir == "" || *origin == "" || *resources == "" {
		return fmt.Errorf("prepare requires --game-dir, --server-origin and --resource-dir")
	}
	versions, err := clientconfig.ReleaseVersionsBesideExecutable()
	if err != nil {
		return err
	}
	settings, err := clientconfig.Normalize(clientconfig.Settings{ServerOrigin: *origin, CDNMode: clientconfig.CDNLocal, LocalResourceDirectory: *resources})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := clientsetup.FetchResourcePolicy(ctx, nil, settings, versions); err != nil {
		return err
	}
	if _, err := clientsetup.Patch(*gameDir, settings, versions); err != nil {
		return err
	}
	if _, err := clientsetup.InstallPlugins(*gameDir, settings, versions, "", "", "", ""); err != nil {
		return err
	}
	if err := clientconfig.SavePreferences(*gameDir); err != nil {
		return err
	}
	fmt.Println("Client prepared:", versions.GameVersion)
	fmt.Println("Server:", settings.ServerOrigin)
	return nil
}
