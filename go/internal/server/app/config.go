package app

import (
	"bd2server/internal/server/design/gameconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/events/calendar"
	"bd2server/internal/server/gateway/authconfig"
	"bd2server/internal/server/gateway/bootstrap"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/resources/policy"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

type configuration struct {
	logLevel           string
	logColor           string
	versionConfigPath  string
	authConfigPath     string
	resourceConfigPath string
	gameConfigPath     string
	listen             string
	dataDir            string
	gameDataVersion    string
	gameDataOrigin     string
	accountSeed        string
	playerSeed         string
	readonlySeed       string
	mailSeed           string
	mailGrantSpool     string
	stateDirectory     string
	deckSeed           string
	worldSeed          string
	devToolsConfig     string
	versions           versionconfig.Config
	calendars          *calendar.Set
	gameRules          gameconfig.Config
	authentication     authconfig.Config
	authRuntime        authconfig.Runtime
	publicResources    resourcepolicy.Public
	bootstrap          bootstrap.Config
	gameData           string
	verifiedGameData   gamedata.Result
}

func loadConfiguration(args []string) (result *configuration, loadErr error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	logLevel := fs.String("log-level", "", "log threshold: trace, debug, info, warn, error (default BD2_LOG_LEVEL or info)")
	logColor := fs.String("log-color", "", "level colors: auto, always, never (default BD2_LOG_COLOR or auto)")
	versionConfigPath := fs.String("version-config", "", "repository versions.json override")
	authConfigPath := fs.String("authentication-config", "", "authentication.json override for development")
	resourceConfigPath := fs.String("resource-config", "", "resources.json override for development")
	gameConfigPath := fs.String("game-config", "", "game.json server gameplay configuration override")
	listen := fs.String("listen", "127.0.0.1:8080", "local listen address")
	dataDir := fs.String("data-dir", "", "server data directory (defaults beside the executable)")
	gameDataVersion := fs.String("game-data-version", "", "validated GameData version (defaults to versions.json)")
	gameDataOrigin := fs.String("game-data-origin", resourcepolicy.OfficialGameDataURL, "official GameData repair source override for development")
	accountSeed := fs.String("account-seed", "", "versioned local account seed")
	playerSeed := fs.String("player-seed", "", "versioned starter inventory and characters")
	readonlySeed := fs.String("readonly-seed", "", "versioned static protocol defaults")
	mailSeed := fs.String("mail-seed", "", "versioned starter mailbox")
	mailGrantSpool := fs.String("mail-grant-spool", "", "optional local JSON spool for idempotent dynamic system mail")
	stateDirectory := fs.String("state-dir", "", "player and shared state directory")
	deckSeed := fs.String("deck-seed", "", "versioned starter deck")
	worldSeed := fs.String("world-seed", "", "versioned starter world")
	devToolsConfig := fs.String("dev-tools-config", "", "development-tool settings JSON (defaults to DATA_DIR/dev-tools.json)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if err := ConfigureLogging(os.Stderr, *logLevel, *logColor); err != nil {
		return nil, err
	}
	var versions versionconfig.Config
	var err error
	if *versionConfigPath == "" {
		versions, err = versionconfig.Find()
	} else {
		versions, err = versionconfig.Load(*versionConfigPath)
	}
	if err != nil {
		return nil, err
	}
	if *gameDataVersion == "" {
		*gameDataVersion = versions.GameDataVersion
	} else {
		// Preserve the development override as part of the effective process
		// configuration so state snapshots describe the GameData actually used.
		versions.GameDataVersion = *gameDataVersion
		if err := versions.Validate(); err != nil {
			return nil, fmt.Errorf("effective version config: %w", err)
		}
	}
	versionconfig.Use(versions)
	calendarDirectory := versions.Resolve("schedules")
	calendars, err := calendar.LoadDirectory(calendarDirectory, versions.GameVersion, versions.GameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load project calendars: %w", err)
	}
	if calendars.RegularService == nil || calendars.MonsterHunt == nil || len(calendars.MonsterHunt.Seasons) == 0 {
		return nil, errors.New("project calendars require regular content and monster hunt schedules")
	}
	slog.Info("project calendars loaded", "directory", calendarDirectory, "revisions", calendars.Revisions, "events", len(calendars.Events))
	if *gameConfigPath == "" {
		*gameConfigPath, err = gameconfig.BesideExecutable()
		if err != nil {
			return nil, err
		}
	}
	gameRules, err := gameconfig.Load(*gameConfigPath)
	if err != nil {
		return nil, err
	}
	if *authConfigPath == "" {
		*authConfigPath, err = authconfig.BesideExecutable()
		if err != nil {
			return nil, err
		}
	}
	authentication, err := authconfig.Load(*authConfigPath)
	if err != nil {
		return nil, err
	}
	authRuntime, err := authentication.ResolveEnvironment()
	if err != nil {
		return nil, err
	}
	defer func() {
		if result == nil {
			clear(authRuntime.MasterKey)
		}
	}()
	if *resourceConfigPath == "" {
		*resourceConfigPath, err = resourcepolicy.BesideExecutable()
		if err != nil {
			return nil, err
		}
	}
	resources, err := resourcepolicy.Load(*resourceConfigPath)
	if err != nil {
		return nil, err
	}
	if *dataDir == "" {
		executable, executableErr := os.Executable()
		if executableErr != nil {
			return nil, fmt.Errorf("resolve server data directory: %w", executableErr)
		}
		*dataDir = filepath.Join(filepath.Dir(executable), "data")
	}
	*dataDir, err = filepath.Abs(filepath.Clean(*dataDir))
	if err != nil {
		return nil, fmt.Errorf("resolve server data directory: %w", err)
	}
	*devToolsConfig = resolveDevelopmentSettingsPath(*dataDir, *devToolsConfig)
	gameData := filepath.Join(*dataDir, "resources", "GameData")
	if *stateDirectory == "" {
		*stateDirectory = filepath.Join(*dataDir, "state")
	}

	seedRoot := versions.Resolve(versions.SeedDirectory)
	for target, name := range map[*string]string{
		accountSeed: "login_user.json", playerSeed: "starter_player.json", readonlySeed: "readonly.json",
		mailSeed: "mail.json", deckSeed: "decks.json", worldSeed: "world.json",
	} {
		if *target == "" {
			*target = filepath.Join(seedRoot, name)
		}
	}
	clientOrigin := "http://" + *listen
	if authentication.Mode == "oauth" {
		clientOrigin = strings.TrimSuffix(authentication.PublicURL, "/")
	}
	base := clientOrigin + "/game/"
	publicResources := resources.Public(versions.BundleVersion, *gameDataVersion)
	cfg := bootstrap.Config{
		BaseURL:     base,
		CDNURL:      publicResources.ServerDataURL,
		Version:     versions.GameVersion,
		BundleVer:   versions.BundleVersion,
		GameDataURL: publicResources.GameDataURL,
		GameDataVer: *gameDataVersion,
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	verifiedGameData, downloaded, err := gamedata.Ensure(context.Background(), nil, gameData, *gameDataVersion, *gameDataOrigin)
	if err != nil {
		return nil, fmt.Errorf("refuse to advertise unavailable or unverified GameData: %w", err)
	}
	if downloaded {
		slog.Info("repaired GameData from official CDN", "archive", verifiedGameData.ArchivePath, "entries", verifiedGameData.EntryCount)
	}
	if err := calendars.ValidateDesign(gameData, *gameDataVersion); err != nil {
		return nil, fmt.Errorf("validate project calendar GameData references: %w", err)
	}
	return &configuration{
		logLevel:           *logLevel,
		logColor:           *logColor,
		versionConfigPath:  *versionConfigPath,
		authConfigPath:     *authConfigPath,
		resourceConfigPath: *resourceConfigPath,
		gameConfigPath:     *gameConfigPath,
		listen:             *listen,
		dataDir:            *dataDir,
		gameDataVersion:    *gameDataVersion,
		gameDataOrigin:     *gameDataOrigin,
		accountSeed:        *accountSeed,
		playerSeed:         *playerSeed,
		readonlySeed:       *readonlySeed,
		mailSeed:           *mailSeed,
		mailGrantSpool:     *mailGrantSpool,
		stateDirectory:     *stateDirectory,
		deckSeed:           *deckSeed,
		worldSeed:          *worldSeed,
		devToolsConfig:     *devToolsConfig,
		versions:           versions, calendars: calendars, gameRules: gameRules, authentication: authentication, authRuntime: authRuntime, publicResources: publicResources, bootstrap: cfg, gameData: gameData, verifiedGameData: verifiedGameData,
	}, nil
}
