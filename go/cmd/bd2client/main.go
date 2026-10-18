package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	clientapp "bd2server/internal/client/app"
	clientconfig "bd2server/internal/client/config"
	"bd2server/internal/client/download"
	clientupdate "bd2server/internal/client/update"
	"bd2server/internal/common/versionconfig"
)

type clientRunOptions struct {
	versionConfigPath   string
	logExecutablePath   string
	localIdentityPlugin string
	loginUIPlugin       string
	cashShopPlugin      string
	fChatPlugin         string
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == clientupdate.HelperFlag {
		if err := clientupdate.RunHelper(os.Args[2:]); err != nil {
			clientapp.ShowFatalError(err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "download-resources" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if err := download.RunResources(ctx, os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "bd2client download-resources:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "prepare" {
		if err := prepareClient(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "bd2client prepare:", err)
			os.Exit(1)
		}
		return
	}
	relaunched, err := relaunchDevelopmentIfNeeded(os.Args[1:])
	if errors.Is(err, context.Canceled) {
		return
	}
	if err != nil {
		clientapp.ShowFatalError(err)
		fmt.Fprintln(os.Stderr, "bd2client:", err)
		os.Exit(1)
	}
	if relaunched {
		return
	}
	handled, err := runClientDevelopmentCommand(os.Args[1:])
	if !handled && err == nil {
		err = runClient(os.Args[1:], clientRunOptions{})
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	if err != nil {
		clientapp.ShowFatalError(err)
		fmt.Fprintln(os.Stderr, "bd2client:", err)
		os.Exit(1)
	}
}

func runClient(args []string, options clientRunOptions) error {
	fs := flag.NewFlagSet("bd2client", flag.ContinueOnError)
	gameDir := fs.String("game-dir", "", "initial Brown Dust II installation directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	logger, logCloser, logPath, err := clientapp.OpenPersistentLogger(options.logExecutablePath)
	if err != nil {
		return err
	}
	defer func() { _ = logCloser.Close() }()
	logger.Info("bd2client starting", "log_path", logPath)

	var versions versionconfig.Config
	if options.versionConfigPath == "" {
		versions, err = clientconfig.ReleaseVersionsBesideExecutable()
	} else {
		versions, err = versionconfig.Load(options.versionConfigPath)
	}
	if err != nil {
		logger.Error("client release version manifest is invalid", "error", err)
		return err
	}
	logger.Info("client release loaded", "client_version", versions.ClientVersion, "game_version", versions.GameVersion)
	if *gameDir == "" {
		preferences, preferenceErr := clientconfig.LoadPreferences()
		if preferenceErr != nil {
			logger.Warn("could not load client preferences", "error", preferenceErr)
		} else {
			*gameDir = preferences.GameDirectory
		}
	}
	var packageDirectory, downloadManifest string
	if options.versionConfigPath != "" {
		packageDirectory = filepath.Dir(options.versionConfigPath)
		downloadManifest = filepath.Join(packageDirectory, "tools", "client", "client-download.json")
	}
	if err := clientapp.Run(clientapp.Options{
		InitialGameDir:      *gameDir,
		Logger:              logger,
		LogPath:             logPath,
		Versions:            versions,
		Development:         options.versionConfigPath != "",
		PackageDirectory:    packageDirectory,
		DownloadManifest:    downloadManifest,
		LocalIdentityPlugin: options.localIdentityPlugin,
		LoginUIPlugin:       options.loginUIPlugin,
		CashShopPlugin:      options.cashShopPlugin,
		FChatPlugin:         options.fChatPlugin,
	}); err != nil {
		logger.Error("bd2client stopped with an error", "error", err)
		return err
	}
	logger.Info("bd2client exited")
	return nil
}
