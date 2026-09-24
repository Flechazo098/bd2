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
	"bd2server/internal/accountstate"
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
	"bd2server/internal/versionconfig"
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

func serve(args []string) (serveErr error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	versionConfigPath := fs.String("version-config", "", "repository versions.json override")
	listen := fs.String("listen", "127.0.0.1:8080", "local listen address")
	cdn := fs.String("cdn", "", "ServerData root (required)")
	gameData := fs.String("game-data", "", "versioned GameData root (required)")
	gameDataVersion := fs.String("game-data-version", "", "validated GameData version (defaults to versions.json)")
	gameDataOrigin := fs.String("game-data-origin", "https://dl.bd2.pmang.cloud/GameData", "official repair source used only when local validation fails")
	accountSeed := fs.String("account-seed", "", "versioned local account seed")
	playerSeed := fs.String("player-seed", "", "versioned starter inventory and characters")
	readonlySeed := fs.String("readonly-seed", "", "versioned server schedules and optional feature defaults")
	mailSeed := fs.String("mail-seed", "", "versioned starter mailbox")
	stateFile := fs.String("state", `..\data\state\state.db`, "local account SQLite database")
	deckSeed := fs.String("deck-seed", "", "versioned starter deck")
	worldSeed := fs.String("world-seed", "", "versioned starter world")
	gachaScheduleSeed := fs.String("gacha-schedule-seed", "", "versioned dynamic gacha schedule")
	gameDir := fs.String("game-dir", "", "Brown Dust II client directory (required)")
	identityPlugin := fs.String("identity-plugin", "", "optional BD2LocalIdentity.dll override for development")
	devToolsConfig := fs.String("dev-tools-config", "", "optional local development-tool settings JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var versions versionconfig.Config
	var err error
	if *versionConfigPath == "" {
		versions, err = versionconfig.Find()
	} else {
		versions, err = versionconfig.Load(*versionConfigPath)
	}
	if err != nil {
		return err
	}
	if *gameDataVersion == "" {
		*gameDataVersion = versions.GameDataVersion
	} else {
		// Preserve the development override as part of the effective process
		// configuration so state snapshots describe the GameData actually used.
		versions.GameDataVersion = *gameDataVersion
		if err := versions.Validate(); err != nil {
			return fmt.Errorf("effective version config: %w", err)
		}
	}
	versionconfig.Use(versions)
	seedRoot := versions.Resolve(versions.SeedDirectory)
	for target, name := range map[*string]string{
		accountSeed: "login_user.json", playerSeed: "starter_player.json", readonlySeed: "readonly.json",
		mailSeed: "mail.json", deckSeed: "decks.json", worldSeed: "world.json", gachaScheduleSeed: "gacha_schedule.json",
	} {
		if *target == "" {
			*target = filepath.Join(seedRoot, name)
		}
	}
	if *cdn == "" || *gameData == "" || *gameDir == "" {
		return errors.New("serve requires --game-dir, --cdn, and --game-data")
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
		Version:     versions.ClientVersion,
		BundleVer:   versions.BundleVersion,
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
	if login.Version != versions.ProtocolVersion || starter.Version != versions.ProtocolVersion {
		return fmt.Errorf("protocol version %s requires matching account and player seeds (got %s and %s)", versions.ProtocolVersion, login.Version, starter.Version)
	}
	gachaSchedule, err := gacha.LoadScheduleSeed(filepath.Clean(*gachaScheduleSeed), versions.ClientVersion)
	if err != nil {
		return fmt.Errorf("load gacha schedule: %w", err)
	}
	var scheduleGroupIDs, stepUpGroupIDs []uint64
	for _, window := range gachaSchedule.Schedules {
		scheduleGroupIDs = append(scheduleGroupIDs, window.GroupID)
	}
	for _, window := range gachaSchedule.StepUps {
		stepUpGroupIDs = append(stepUpGroupIDs, window.GroupID)
	}
	regularGacha, equipmentGacha, err := gamedata.LoadActiveGachaForSchedules(filepath.Clean(*gameData), *gameDataVersion, scheduleGroupIDs, stepUpGroupIDs)
	if err != nil {
		return fmt.Errorf("load active gacha GameData: %w", err)
	}
	stateRepository, err := accountstate.Open(filepath.Clean(*stateFile))
	if err != nil {
		return fmt.Errorf("open account state database: %w", err)
	}
	defer stateRepository.Close()
	accountDomains := []string{"characters", "collection", "deck", "equipment", "items", "mail", "missions", "progress", "wallet"}
	if !stateRepository.IsNew() {
		if err := stateRepository.RequireDomains(accountDomains...); err != nil {
			return fmt.Errorf("reject incomplete account database: %w", err)
		}
	}
	startupTransaction, err := stateRepository.BeginOperation()
	if err != nil {
		return fmt.Errorf("begin startup state transaction: %w", err)
	}
	startupCommitted := false
	defer func() {
		if startupCommitted {
			return
		}
		if rollbackErr := startupTransaction.Rollback(); rollbackErr != nil {
			serveErr = errors.Join(serveErr, rollbackErr)
		}
	}()
	serverConfig, err := readonly.Load(filepath.Clean(*readonlySeed))
	if err != nil {
		return fmt.Errorf("load readonly server configuration: %w", err)
	}
	mailbox, err := mail.Load(filepath.Clean(*mailSeed))
	if err != nil {
		return fmt.Errorf("load starter mailbox: %w", err)
	}
	progressState, err := progress.OpenStore(stateRepository)
	if err != nil {
		return err
	}
	deckConfig, err := deck.LoadSeed(filepath.Clean(*deckSeed))
	if err != nil {
		return fmt.Errorf("load starter deck: %w", err)
	}
	deckStateStore, err := deck.OpenStore(stateRepository, deckConfig)
	if err != nil {
		return fmt.Errorf("load deck state: %w", err)
	}
	if err := login.AttachPresetSlots(deckStateStore); err != nil {
		return fmt.Errorf("attach preset slots to login: %w", err)
	}
	ownedItems, err := player.OpenInventory(stateRepository, starter)
	if err != nil {
		return fmt.Errorf("load owned inventory: %w", err)
	}
	randomBoxes, err := gamedata.LoadRandomBoxDesign(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load deterministic random-box GameData: %w", err)
	}
	if err := ownedItems.AttachRandomBoxes(randomBoxes); err != nil {
		return fmt.Errorf("attach random-box GameData: %w", err)
	}
	gold, freeJewelry, jewelry, mileage, err := login.SeedCurrencies()
	if err != nil {
		return fmt.Errorf("read account seed currency: %w", err)
	}
	hopePowder, err := login.SeedHopePowder()
	if err != nil {
		return fmt.Errorf("read account seed hope powder: %w", err)
	}
	catalyst, err := login.SeedCatalyst()
	if err != nil {
		return fmt.Errorf("read account seed catalyst: %w", err)
	}
	equipMileage, equipMileageExchangeGage, err := login.SeedEquipmentMileage()
	if err != nil {
		return fmt.Errorf("read account seed equipment mileage: %w", err)
	}
	wallet, err := player.OpenWallet(stateRepository, player.Currency{
		Gold: gold, FreeJewelry: freeJewelry, Jewelry: jewelry, Catalyst: catalyst, Mileage: mileage, HopePowder: hopePowder,
		EquipMileage: equipMileage, EquipMileageExchangeGage: equipMileageExchangeGage,
	})
	if err != nil {
		return fmt.Errorf("load wallet state: %w", err)
	}
	if err := login.AttachCurrencies(wallet); err != nil {
		return fmt.Errorf("attach wallet to login: %w", err)
	}
	slotDesign, err := gamedata.LoadInventorySlotDesign(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load inventory slot GameData: %w", err)
	}
	itemSlots, storageSlots, equipmentInventorySlots, equipmentStorageSlots, err := login.SeedInventorySlots()
	if err != nil {
		return fmt.Errorf("read account seed inventory slots: %w", err)
	}
	inventorySlots, err := player.OpenInventorySlots(stateRepository, slotDesign, player.InventorySlotCounts{
		Items: itemSlots, Storage: storageSlots, Equipment: equipmentInventorySlots, EquipmentStorage: equipmentStorageSlots,
	}, wallet)
	if err != nil {
		return fmt.Errorf("load inventory slot state: %w", err)
	}
	if *devToolsConfig != "" {
		inventorySlots.AttachDevelopmentSettings(filepath.Clean(*devToolsConfig))
	}
	if err := login.AttachInventorySlots(inventorySlots); err != nil {
		return fmt.Errorf("attach inventory slots to login: %w", err)
	}
	mailService, err := mail.OpenService(stateRepository, mailbox, ownedItems, wallet)
	if err != nil {
		return fmt.Errorf("load mail state: %w", err)
	}
	if err := mailService.AttachSeedPath(filepath.Clean(*mailSeed)); err != nil {
		return fmt.Errorf("watch mail seed: %w", err)
	}
	missionDesign, err := gamedata.LoadMissionDesign(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load mission GameData: %w", err)
	}
	missionService, err := missions.Open(stateRepository, missionDesign, ownedItems)
	if err != nil {
		return fmt.Errorf("load mission state: %w", err)
	}
	if err := missionService.AttachWallet(wallet); err != nil {
		return fmt.Errorf("attach mission wallet: %w", err)
	}
	if err := missionService.AttachMail(mailService); err != nil {
		return fmt.Errorf("attach mission compensation mailbox: %w", err)
	}
	if err := missionService.CompleteMission(gamedata.MissionKey{GroupType: 0, GroupID: 1, ID: 101}); err != nil {
		return fmt.Errorf("record daily login mission: %w", err)
	}
	if err := missionService.SetProgress(gamedata.MissionKey{GroupType: 1, GroupID: 2, ID: 201}, 1); err != nil {
		return fmt.Errorf("record weekly login mission: %w", err)
	}
	ownedEquipment, err := player.OpenEquipmentInventory(stateRepository)
	if err != nil {
		return fmt.Errorf("load owned equipment: %w", err)
	}
	equipmentSlots, err := gamedata.LoadEquipmentSlots(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load equipment slot GameData: %w", err)
	}
	if err := ownedEquipment.AttachSlots(equipmentSlots); err != nil {
		return fmt.Errorf("attach equipment slot GameData: %w", err)
	}
	equipmentUpgrade, err := gamedata.LoadEquipmentUpgradeDesign(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load equipment upgrade GameData: %w", err)
	}
	if err := ownedEquipment.AttachUpgrade(equipmentUpgrade, wallet, ownedItems); err != nil {
		return fmt.Errorf("attach equipment upgrade GameData: %w", err)
	}
	equipmentCraft, err := gamedata.LoadEquipmentCraftDesign(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load equipment crafting GameData: %w", err)
	}
	talentGrowth, err := gamedata.LoadTalentGrowthDesign(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load talent growth GameData: %w", err)
	}
	if err := ownedEquipment.AttachCraft(equipmentCraft); err != nil {
		return fmt.Errorf("attach equipment crafting GameData: %w", err)
	}
	equipmentSmelting, err := gamedata.LoadEquipmentSmeltingDesign(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load equipment smelting GameData: %w", err)
	}
	if err := ownedEquipment.AttachSmelting(equipmentSmelting, wallet, ownedItems); err != nil {
		return fmt.Errorf("attach equipment smelting GameData: %w", err)
	}
	equipmentOptionReroll, err := gamedata.LoadEquipmentOptionRerollDesign(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load equipment option reroll GameData: %w", err)
	}
	if err := ownedEquipment.AttachOptionReroll(equipmentOptionReroll, wallet, ownedItems); err != nil {
		return fmt.Errorf("attach equipment option reroll GameData: %w", err)
	}
	collection, err := player.OpenCollectionStore(stateRepository, starter.Costumes)
	if err != nil {
		return fmt.Errorf("load owned collection: %w", err)
	}
	infiniteGacha, err := gamedata.LoadInfiniteGacha(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load infinite gacha GameData: %w", err)
	}
	gachaService, err := gacha.NewService(infiniteGacha, regularGacha, collection, wallet)
	if err != nil {
		return err
	}
	if err := login.AttachPurchaseCounts(gachaService); err != nil {
		return fmt.Errorf("attach cash purchase counts to login: %w", err)
	}
	if err := gachaService.AttachSchedule(gachaSchedule); err != nil {
		return fmt.Errorf("attach gacha schedule: %w", err)
	}
	previewEventIndex, err := serverConfig.CashProductEventIndex(gamedata.InfiniteProductGroupID, gamedata.InfiniteProductID)
	if err != nil {
		return fmt.Errorf("load infinite preview event: %w", err)
	}
	if err := gachaService.AttachPreviewEventIndex(previewEventIndex); err != nil {
		return fmt.Errorf("attach infinite preview event: %w", err)
	}
	// The mapped client property is IsDoneFirstGachaPick. Its authoritative
	// local state is the explicit GachaSubType=3 completion marker.
	login.SetFirstGacha(gachaService.FirstGachaCompleted())
	gachaService.AttachInventory(ownedItems)
	gachaService.AttachEquipmentGacha(equipmentGacha, ownedEquipment)
	gachaService.AttachPreviewMission(func() error {
		return missionService.CompleteMission(gamedata.MissionKey{GroupType: 0, GroupID: 1, ID: 111})
	})
	worldService, err := world.Load(filepath.Clean(*worldSeed), filepath.Clean(*gameData), *gameDataVersion,
		stateRepository, progressState, starter, ownedEquipment, ownedItems, wallet)
	if err != nil {
		return fmt.Errorf("load world state: %w", err)
	}
	if err := collection.BindBaseCharacters(worldService.CharacterService().RawAll()); err != nil {
		return fmt.Errorf("bind base collection characters: %w", err)
	}
	if reward, earned := worldService.EarnedQuestCostume(); earned {
		if err := collection.AttachRewardCostume(reward); err != nil {
			return fmt.Errorf("attach earned quest costume: %w", err)
		}
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
	if err := deckStateStore.AttachPresetRuntime(wallet, worldService.CharacterService(), ownedEquipment, collection); err != nil {
		return fmt.Errorf("attach ordinary preset runtime: %w", err)
	}
	pictorialDesign, err := gamedata.LoadPictorialDesign(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load pictorial GameData: %w", err)
	}
	pictorialService := &pictorial.Service{Design: pictorialDesign, Owned: worldService}
	charAwakeDesign, err := gamedata.LoadCharAwakeDesign(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load character awakening GameData: %w", err)
	}
	charAwakeService, err := player.NewCharAwakeService(charAwakeDesign, collection, worldService.CharacterService(), ownedItems, wallet)
	if err != nil {
		return err
	}
	pictorialService.AwakeContributions = charAwakeService.Contributions
	if err := worldService.CharacterService().AttachMaxHealth(pictorialService.MaxHealth); err != nil {
		return fmt.Errorf("attach pictorial character stats: %w", err)
	}
	if err := worldService.CharacterService().AttachWallet(wallet); err != nil {
		return fmt.Errorf("attach character promotion wallet: %w", err)
	}
	if err := worldService.CharacterService().AttachTalentGrowth(talentGrowth); err != nil {
		return fmt.Errorf("attach character talent growth: %w", err)
	}
	costumePotentialDesign, err := gamedata.LoadCostumePotentialDesign(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load costume potential GameData: %w", err)
	}
	costumePotentialService, err := player.NewCostumePotentialService(costumePotentialDesign, collection, worldService.CharacterService(), ownedItems, wallet)
	if err != nil {
		return err
	}
	battleService := battle.NewService(filepath.Clean(*gameData), *gameDataVersion, ownedItems, worldService.CurrentPackID)
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
		inventorySlots,
		charAwakeService,
		costumePotentialService,
		starter,
		mailService,
		gachaService,
		missionService,
		pictorialService,
		schedule.Current(),
		readonly.Service{Seed: serverConfig},
		feature.Service{},
	)
	if err != nil {
		return err
	}
	if stateRepository.IsNew() {
		if err := ensureAccountStateInitialized(
			progressState, deckStateStore, ownedItems, ownedEquipment,
			worldService.CharacterService(), collection, wallet, inventorySlots, mailService, missionService,
		); err != nil {
			return fmt.Errorf("initialize complete account state generation: %w", err)
		}
	}
	problems, err := stateRepository.Validate()
	if err != nil {
		return fmt.Errorf("validate account state database: %w", err)
	}
	if len(problems) != 0 {
		return stateProblemsError("account state database rejected", problems)
	}
	if err := startupTransaction.Commit(); err != nil {
		return fmt.Errorf("commit startup state transaction: %w", err)
	}
	startupCommitted = true
	if err := stateRepository.Check(); err != nil {
		return fmt.Errorf("startup state transaction requires recovery restart: %w", err)
	}
	if err := game.AttachStateStore(stateRepository); err != nil {
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

type accountStateInitializer interface {
	EnsurePersisted() error
}

func ensureAccountStateInitialized(stores ...accountStateInitializer) error {
	for _, store := range stores {
		if store == nil {
			return errors.New("nil account state initializer")
		}
		if err := store.EnsurePersisted(); err != nil {
			return err
		}
	}
	return nil
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
	fmt.Fprintln(os.Stderr, `bd2server - BrownDust II local development server

Usage:
  bd2server serve --game-dir DIR --cdn DIR --game-data DIR [--version-config FILE] [options]
  bd2server patch-client --game-dir DIR [options]
  bd2server state check [options]

The server binds to loopback by default and is intended for local research.`)
}
