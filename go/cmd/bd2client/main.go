package main

import (
	"flag"
	"fmt"
	"os"

	clientapp "bd2server/internal/client/app"
	clientconfig "bd2server/internal/client/config"
)

type clientRunOptions struct {
	versionConfigPath   string
	logExecutablePath   string
	localIdentityPlugin string
	loginUIPlugin       string
}

func main() {
	relaunched, err := relaunchDevelopmentIfNeeded(os.Args[1:])
	if err != nil {
		clientapp.ShowFatalError(err)
		fmt.Fprintln(os.Stderr, "bd2client:", err)
		os.Exit(1)
	}
	if relaunched {
		return
	}
	args, options, err := developmentRunOptions(os.Args[1:])
	if err == nil {
		err = runClient(args, options)
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
	defer logCloser.Close()
	logger.Info("bd2client starting", "log_path", logPath)

	var versions clientconfig.ReleaseVersions
	if options.versionConfigPath == "" {
		versions, err = clientconfig.ReleaseVersionsBesideExecutable()
	} else {
		versions, err = clientconfig.LoadReleaseVersions(options.versionConfigPath)
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
	if err := clientapp.Run(clientapp.Options{
		InitialGameDir:      *gameDir,
		Logger:              logger,
		LogPath:             logPath,
		Versions:            versions,
		LocalIdentityPlugin: options.localIdentityPlugin,
		LoginUIPlugin:       options.loginUIPlugin,
	}); err != nil {
		logger.Error("bd2client stopped with an error", "error", err)
		return err
	}
	logger.Info("bd2client exited")
	return nil
}
