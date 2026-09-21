package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"bd2server/internal/account"
	"bd2server/internal/battle"
	"bd2server/internal/bootstrap"
	"bd2server/internal/clientplugin"
	"bd2server/internal/deck"
	"bd2server/internal/feature"
	"bd2server/internal/gacha"
	"bd2server/internal/gamedata"
	"bd2server/internal/introdb"
	"bd2server/internal/mail"
	"bd2server/internal/missions"
	"bd2server/internal/pictorial"
	"bd2server/internal/player"
	"bd2server/internal/progress"
	"bd2server/internal/readonly"
	"bd2server/internal/schedule"
	"bd2server/internal/session"
	"bd2server/internal/transport"
	"bd2server/internal/world"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "patch-client":
		err = patchClient(os.Args[2:])
	case "state":
		err = stateCommand(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		slog.Error("command failed", "command", os.Args[1], "error", err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "local listen address")
	cdn := fs.String("cdn", "", "ServerData root (required)")
	gameData := fs.String("game-data", "", "versioned GameData root (required)")
	gameDataVersion := fs.String("game-data-version", "", "validated GameData version (required)")
	gameDataOrigin := fs.String("game-data-origin", "https://dl.bd2.pmang.cloud/GameData", "official repair source used only when local validation fails")
	accountSeed := fs.String("account-seed", `seed\v2_34_13\login_user.json`, "versioned local account seed")
	playerSeed := fs.String("player-seed", `seed\v2_34_13\starter_player.json`, "versioned starter inventory and characters")
	readonlySeed := fs.String("readonly-seed", `seed\v2_34_13\readonly.json`, "versioned server schedules and optional feature defaults")
	mailSeed := fs.String("mail-seed", `seed\v2_34_13\mail.json`, "versioned starter mailbox")
	stateFile := fs.String("state", `..\data\state\progress.json`, "local player progress save")
	deckSeed := fs.String("deck-seed", `seed\v2_34_13\decks.json`, "versioned starter deck")
	deckState := fs.String("deck-state", `..\data\state\deck.json`, "local deck and waypoint save")
	worldSeed := fs.String("world-seed", `seed\v2_34_13\world.json`, "versioned starter world")
	stateTool := fs.String("state-tool", "", "optional Haskell state tool override (release package includes it)")
	gameDir := fs.String("game-dir", "", "Brown Dust II client directory (required)")
	identityPlugin := fs.String("identity-plugin", "", "optional BD2LocalIdentity.dll override for development")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cdn == "" || *gameData == "" || *gameDataVersion == "" || *gameDir == "" {
		return errors.New("serve requires --game-dir, --cdn, --game-data, and --game-data-version")
	}
	packagedPlugin, err := clientplugin.ResolvePackaged(*identityPlugin)
	if err != nil {
		return err
	}
	pluginResult, err := clientplugin.Install(*gameDir, packagedPlugin)
	if err != nil {
		return err
	}
	if pluginResult.Changed {
		slog.Info("installed local identity plugin", "path", pluginResult.Destination)
	} else {
		slog.Info("local identity plugin is current", "path", pluginResult.Destination)
	}
	base := "http://" + *listen + "/game/"
	cfg := bootstrap.Config{
		BaseURL:     base,
		CDNURL:      "http://" + *listen + "/assets/ServerData",
		Version:     bootstrap.ClientVersion,
		BundleVer:   bootstrap.BundleVersion,
		GameDataURL: "http://" + *listen + "/assets/GameData",
		GameDataVer: *gameDataVersion,
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if info, err := os.Stat(*cdn); err != nil || !info.IsDir() {
		return fmt.Errorf("CDN directory is unavailable: %q", *cdn)
	}
	verifiedGameData, downloaded, err := gamedata.Ensure(context.Background(), nil, filepath.Clean(*gameData), *gameDataVersion, *gameDataOrigin)
	if err != nil {
		return fmt.Errorf("refuse to advertise unavailable or unverified GameData: %w", err)
	}
	if downloaded {
		slog.Info("repaired GameData from official CDN", "archive", verifiedGameData.ArchivePath, "entries", verifiedGameData.EntryCount)
	}
	login, err := account.Load(filepath.Clean(*accountSeed))
	if err != nil {
		return fmt.Errorf("load local account seed: %w", err)
	}
	starter, err := player.Load(filepath.Clean(*playerSeed))
	if err != nil {
		return fmt.Errorf("load starter player: %w", err)
	}
	regularGacha, err := gamedata.LoadRegularCostumeGacha(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load regular gacha GameData: %w", err)
	}
	if err := repairStateBeforeServe(filepath.Dir(filepath.Clean(*stateFile)), *stateTool, *gameDataVersion, regularGacha, starter); err != nil {
		return fmt.Errorf("state migration/repair: %w", err)
	}
	serverConfig, err := readonly.Load(filepath.Clean(*readonlySeed))
	if err != nil {
		return fmt.Errorf("load readonly server configuration: %w", err)
	}
	mailbox, err := mail.Load(filepath.Clean(*mailSeed))
	if err != nil {
		return fmt.Errorf("load starter mailbox: %w", err)
	}
	progressState, err := progress.OpenStore(*stateFile)
	if err != nil {
		return err
	}
	// The current local save has completed the first-gacha tutorial gate.
	// Deriving this UI flag from authoritative story progress prevents a new
	// account from seeing ordinary pools before the tutorial unlocks them.
	login.SetFirstGacha(progressState.QuestCleared(37, 21))
	deckConfig, err := deck.LoadSeed(filepath.Clean(*deckSeed))
	if err != nil {
		return fmt.Errorf("load starter deck: %w", err)
	}
	deckStateStore, err := deck.OpenStore(filepath.Clean(*deckState), deckConfig)
	if err != nil {
		return fmt.Errorf("load deck state: %w", err)
	}
	ownedItems, err := player.OpenInventory(filepath.Join(filepath.Dir(*stateFile), "items.json"), starter)
	if err != nil {
		return fmt.Errorf("load owned inventory: %w", err)
	}
	gold, freeJewelry, jewelry, mileage, err := login.SeedCurrencies()
	if err != nil {
		return fmt.Errorf("read account seed currency: %w", err)
	}
	hopePowder, err := login.SeedHopePowder()
	if err != nil {
		return fmt.Errorf("read account seed hope powder: %w", err)
	}
	wallet, err := player.OpenWallet(filepath.Join(filepath.Dir(*stateFile), "wallet.json"), player.Currency{
		Gold: gold, FreeJewelry: freeJewelry, Jewelry: jewelry, Mileage: mileage, HopePowder: hopePowder,
	})
	if err != nil {
		return fmt.Errorf("load wallet state: %w", err)
	}
	if err := login.AttachCurrencies(wallet); err != nil {
		return fmt.Errorf("attach wallet to login: %w", err)
	}
	mailService, err := mail.OpenService(filepath.Join(filepath.Dir(*stateFile), "mail.json"), mailbox, ownedItems, wallet)
	if err != nil {
		return fmt.Errorf("load mail state: %w", err)
	}
	missionDesign, err := gamedata.LoadMissionDesign(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load mission GameData: %w", err)
	}
	missionService, err := missions.Open(filepath.Join(filepath.Dir(*stateFile), "missions.json"), missionDesign, ownedItems)
	if err != nil {
		return fmt.Errorf("load mission state: %w", err)
	}
	if err := missionService.AttachWallet(wallet); err != nil {
		return fmt.Errorf("attach mission wallet: %w", err)
	}
	if err := missionService.CompleteMission(gamedata.MissionKey{GroupType: 0, GroupID: 1, ID: 101}); err != nil {
		return fmt.Errorf("record daily login mission: %w", err)
	}
	if err := missionService.SetProgress(gamedata.MissionKey{GroupType: 1, GroupID: 2, ID: 201}, 1); err != nil {
		return fmt.Errorf("record weekly login mission: %w", err)
	}
	ownedEquipment, err := player.OpenEquipmentInventory(filepath.Join(filepath.Dir(*stateFile), "equipment.json"))
	if err != nil {
		return fmt.Errorf("load owned equipment: %w", err)
	}
	collection, err := player.OpenCollectionStore(filepath.Join(filepath.Dir(*stateFile), "collection.json"), starter.Costumes)
	if err != nil {
		return fmt.Errorf("load owned collection: %w", err)
	}
	infiniteGacha, err := gamedata.LoadInfiniteGacha(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load infinite gacha GameData: %w", err)
	}
	equipmentGacha, err := gamedata.LoadEquipmentGacha(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load equipment gacha GameData: %w", err)
	}
	gachaService, err := gacha.NewService(infiniteGacha, regularGacha, collection, wallet)
	if err != nil {
		return err
	}
	gachaService.AttachInventory(ownedItems)
	gachaService.AttachEquipmentGacha(equipmentGacha, ownedEquipment)
	gachaService.AttachPreviewMission(func() error {
		return missionService.CompleteMission(gamedata.MissionKey{GroupType: 0, GroupID: 1, ID: 111})
	})
	worldService, err := world.Load(filepath.Clean(*worldSeed), filepath.Clean(*gameData), *gameDataVersion,
		filepath.Join(filepath.Dir(*stateFile), "characters.json"), progressState, starter, ownedEquipment, ownedItems, wallet)
	if err != nil {
		return fmt.Errorf("load world state: %w", err)
	}
	if err := collection.BindBaseCharacters(worldService.CharacterService().RawAll()); err != nil {
		return fmt.Errorf("bind base collection characters: %w", err)
	}
	if err := worldService.AttachCollection(collection); err != nil {
		return fmt.Errorf("attach gacha collection state: %w", err)
	}
	if err := worldService.AttachDecks(deckStateStore); err != nil {
		return fmt.Errorf("attach world deck state: %w", err)
	}
	if err := ownedEquipment.AttachCharacters(worldService.CharacterService()); err != nil {
		return fmt.Errorf("attach equipment character state: %w", err)
	}
	pictorialDesign, err := gamedata.LoadPictorialDesign(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load pictorial GameData: %w", err)
	}
	pictorialService := &pictorial.Service{Design: pictorialDesign, Owned: worldService}
	if err := worldService.CharacterService().AttachMaxHealth(pictorialService.MaxHealth); err != nil {
		return fmt.Errorf("attach pictorial character stats: %w", err)
	}
	battleService := battle.NewService(filepath.Clean(*gameData), *gameDataVersion, ownedItems)
	battleService.AttachTutorialWin(func() error {
		return missionService.CompleteMission(gamedata.MissionKey{GroupType: 0, GroupID: 1, ID: 113})
	})
	battleService.AttachPictorialBuffs(func() ([]gamedata.PictorialBuffStat, error) {
		_, buffs, err := pictorialService.Snapshot()
		return buffs, err
	})
	game, err := session.NewServerWithProgress(login, progressState,
		battleService,
		worldService,
		worldService.CharacterService(),
		progressState,
		deckStateStore,
		ownedItems,
		ownedEquipment,
		starter,
		mailService,
		gachaService,
		missionService,
		pictorialService,
		schedule.Version23413(),
		readonly.Service{Seed: serverConfig},
		feature.Service{},
	)
	if err != nil {
		return err
	}
	dispatcher := transport.Bootstrap{Config: cfg}
	handler := transport.HTTP{Dispatcher: dispatcher, Raw: game, CDNDir: filepath.Clean(*cdn), GameDataDir: filepath.Clean(*gameData)}.Handler()
	server := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	slog.Info("BD2 local server listening", "address", *listen, "client", cfg.Version, "bundle", cfg.BundleVer, "cdn", *cdn, "gameData", verifiedGameData.ArchivePath, "gameDataEntries", verifiedGameData.EntryCount, "accountSeed", *accountSeed)
	return server.ListenAndServe()
}

func patchClient(args []string) error {
	fs := flag.NewFlagSet("patch-client", flag.ContinueOnError)
	gameDir := fs.String("game-dir", "", "BrownDust II game directory (required)")
	serverURL := fs.String("url", "http://127.0.0.1:8080/game/", "exactly 27-byte replacement LIVE_URL")
	verify := fs.Bool("verify", false, "inspect the embedded LIVE_URL without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *gameDir == "" {
		return errors.New("patch-client requires --game-dir")
	}
	if *verify {
		result, err := introdb.VerifyClient(*gameDir)
		if err != nil {
			return err
		}
		fmt.Printf("resources.assets: %s\nTextAsset pathID: %d\nLIVE_URL: %s\n", result.AssetsPath, result.ObjectPath, result.URL)
		return nil
	}
	result, err := introdb.PatchClient(*gameDir, *serverURL)
	if err != nil {
		return err
	}
	disabled, err := disableLegacyPlugin(*gameDir)
	if err != nil {
		return err
	}
	verified, err := introdb.VerifyClient(*gameDir)
	if err != nil {
		return fmt.Errorf("post-patch verification: %w", err)
	}
	fmt.Printf("patched: %s\nbackup: %s\nLIVE_URL: %s\n", result.AssetsPath, result.BackupPath, verified.URL)
	if disabled != "" {
		fmt.Printf("legacy plugin disabled: %s\n", disabled)
	}
	return nil
}

func disableLegacyPlugin(gameDir string) (string, error) {
	source := filepath.Join(gameDir, "BepInEx", "plugins", "PluginLocalRes.dll")
	destination := filepath.Join(gameDir, "BepInEx", "disabled", "PluginLocalRes.dll")
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("inspect legacy local-resource plugin: %w", err)
	}
	if _, err := os.Stat(destination); err == nil {
		return "", fmt.Errorf("legacy plugin exists at both active and disabled paths")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(source, destination); err != nil {
		return "", fmt.Errorf("disable legacy local-resource plugin: %w", err)
	}
	return destination, nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `bd2server - BrownDust II 2.34.13 local development server

Usage:
  bd2server serve --game-dir DIR --cdn DIR --game-data DIR --game-data-version VERSION [options]
  bd2server patch-client --game-dir DIR [options]
  bd2server state check [options]
  bd2server state migrate-v1-v2 [options]
  bd2server state repair [options]

The server binds to loopback by default and is intended for local research.`)
}
