package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"bd2server/internal/server/account"
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/auth"
	"bd2server/internal/server/authconfig"
	"bd2server/internal/server/battle"
	"bd2server/internal/server/bootstrap"
	"bd2server/internal/server/deck"
	"bd2server/internal/server/feature"
	"bd2server/internal/server/gacha"
	"bd2server/internal/server/gameconfig"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/lifecycle"
	"bd2server/internal/server/mail"
	"bd2server/internal/server/missions"
	"bd2server/internal/server/pictorial"
	"bd2server/internal/server/player"
	"bd2server/internal/server/progress"
	"bd2server/internal/server/readonly"
	"bd2server/internal/server/resourcefetch"
	"bd2server/internal/server/resourcepolicy"
	"bd2server/internal/server/schedule"
	"bd2server/internal/server/session"
	"bd2server/internal/server/transport"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/world"
)

func main() {
	if handled, err := runDevelopmentCommand(os.Args[1:]); handled {
		if err != nil {
			slog.Error("development command failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "state":
		err = stateCommand(os.Args[2:])
	case "resources":
		err = resourcesCommand(os.Args[2:])
	case "preflight":
		err = preflight(os.Args[2:])
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
	authConfigPath := fs.String("authentication-config", "", "authentication.json override for development")
	resourceConfigPath := fs.String("resource-config", "", "resources.json override for development")
	gameConfigPath := fs.String("game-config", "", "game.json server gameplay configuration override")
	listen := fs.String("listen", "127.0.0.1:8080", "local listen address")
	dataDir := fs.String("data-dir", "", "server data directory (defaults beside the executable)")
	gameDataVersion := fs.String("game-data-version", "", "validated GameData version (defaults to versions.json)")
	gameDataOrigin := fs.String("game-data-origin", resourcepolicy.OfficialGameDataURL, "official GameData repair source override for development")
	accountSeed := fs.String("account-seed", "", "versioned local account seed")
	playerSeed := fs.String("player-seed", "", "versioned starter inventory and characters")
	readonlySeed := fs.String("readonly-seed", "", "versioned server schedules and optional feature defaults")
	mailSeed := fs.String("mail-seed", "", "versioned starter mailbox")
	mailGrantSpool := fs.String("mail-grant-spool", "", "optional local JSON spool for idempotent dynamic system mail")
	stateFile := fs.String("state", "", "account SQLite database override")
	deckSeed := fs.String("deck-seed", "", "versioned starter deck")
	worldSeed := fs.String("world-seed", "", "versioned starter world")
	gachaScheduleSeed := fs.String("gacha-schedule-seed", "", "versioned dynamic gacha schedule")
	devToolsConfig := fs.String("dev-tools-config", "", "development-tool settings JSON (defaults to DATA_DIR/dev-tools.json)")
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
	if *gameConfigPath == "" {
		*gameConfigPath, err = gameconfig.BesideExecutable()
		if err != nil {
			return err
		}
	}
	gameRules, err := gameconfig.Load(*gameConfigPath)
	if err != nil {
		return err
	}
	if *authConfigPath == "" {
		*authConfigPath, err = authconfig.BesideExecutable()
		if err != nil {
			return err
		}
	}
	authentication, err := authconfig.Load(*authConfigPath)
	if err != nil {
		return err
	}
	authRuntime, err := authentication.ResolveEnvironment()
	if err != nil {
		return err
	}
	defer clear(authRuntime.MasterKey)
	if *resourceConfigPath == "" {
		*resourceConfigPath, err = resourcepolicy.BesideExecutable()
		if err != nil {
			return err
		}
	}
	resources, err := resourcepolicy.Load(*resourceConfigPath)
	if err != nil {
		return err
	}
	if *dataDir == "" {
		executable, executableErr := os.Executable()
		if executableErr != nil {
			return fmt.Errorf("resolve server data directory: %w", executableErr)
		}
		*dataDir = filepath.Join(filepath.Dir(executable), "data")
	}
	*dataDir, err = filepath.Abs(filepath.Clean(*dataDir))
	if err != nil {
		return fmt.Errorf("resolve server data directory: %w", err)
	}
	*devToolsConfig = resolveDevelopmentSettingsPath(*dataDir, *devToolsConfig)
	gameData := filepath.Join(*dataDir, "resources", "GameData")
	if *stateFile == "" {
		*stateFile = filepath.Join(*dataDir, "state", "state.db")
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Clean(*stateFile)), 0o755); err != nil {
		return fmt.Errorf("create server state directory: %w", err)
	}
	seedRoot := versions.Resolve(versions.SeedDirectory)
	for target, name := range map[*string]string{
		accountSeed: "login_user.json", playerSeed: "starter_player.json", readonlySeed: "readonly.json",
		mailSeed: "mail.json", deckSeed: "decks.json", worldSeed: "world.json", gachaScheduleSeed: "gacha_schedule.json",
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
		return err
	}
	verifiedGameData, downloaded, err := gamedata.Ensure(context.Background(), nil, gameData, *gameDataVersion, *gameDataOrigin)
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
	if login.Version != versions.GameVersion || starter.Version != versions.GameVersion {
		return fmt.Errorf("game version %s requires matching account and player seeds (got %s and %s)", versions.GameVersion, login.Version, starter.Version)
	}
	gachaSchedule, err := gacha.LoadScheduleSeed(filepath.Clean(*gachaScheduleSeed), versions.GameVersion)
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
	regularGacha, equipmentGacha, err := gamedata.LoadActiveGachaForSchedules(gameData, *gameDataVersion, scheduleGroupIDs, stepUpGroupIDs)
	if err != nil {
		return fmt.Errorf("load active gacha GameData: %w", err)
	}
	if gameRules.Gacha.IncludeCollaborationURWeapons {
		if err := equipmentGacha.IncludeCollaborationURWeapons(gameData, *gameDataVersion); err != nil {
			return fmt.Errorf("apply collaboration UR weapon game rule: %w", err)
		}
	}
	slog.Info("server gameplay rules loaded", "config", *gameConfigPath,
		"include_collaboration_ur_weapons", gameRules.Gacha.IncludeCollaborationURWeapons)
	limitedCostumes, err := gamedata.LoadLimitedCostumes(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load limited costume GameData: %w", err)
	}
	firstGacha, err := gamedata.LoadFirstGacha(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load first gacha GameData: %w", err)
	}
	stateRepository, err := accountstate.Open(filepath.Clean(*stateFile))
	if err != nil {
		return fmt.Errorf("open account state database: %w", err)
	}
	defer stateRepository.Close()
	var authService *auth.Service
	if authentication.Mode == "oauth" {
		authStore, err := auth.Open(filepath.Join(filepath.Dir(filepath.Clean(*stateFile)), "auth.db"), authRuntime.MasterKey)
		if err != nil {
			return fmt.Errorf("open authentication database: %w", err)
		}
		defer authStore.Close()
		authService, err = auth.New(authRuntime, authStore)
		if err != nil {
			return err
		}
	}
	accountDomains := []string{"characters", "collection", "deck", "equipment", "items", "mail", "missions", "progress", "wallet"}
	initializationState, err := stateRepository.InitializationState(accountDomains...)
	if err != nil {
		return fmt.Errorf("reject incomplete account database: %w", err)
	}
	if initializationState == accountstate.InitializationCorrupt {
		return errors.New("reject incomplete account database: corrupt initialization state")
	}
	initializeAccount := initializationState == accountstate.InitializationPending
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
	randomBoxes, err := gamedata.LoadRandomBoxDesign(gameData, *gameDataVersion)
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
	slotDesign, err := gamedata.LoadInventorySlotDesign(gameData, *gameDataVersion)
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
	inventorySlots.AttachDevelopmentSettings(*devToolsConfig)
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
	if *mailGrantSpool != "" {
		if err := mailService.AttachGrantSpoolPath(*mailGrantSpool); err != nil {
			return fmt.Errorf("attach mail grant spool: %w", err)
		}
	}
	missionDesign, err := gamedata.LoadMissionDesign(gameData, *gameDataVersion)
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
	equipmentSlots, err := gamedata.LoadEquipmentSlots(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load equipment slot GameData: %w", err)
	}
	if err := ownedEquipment.AttachSlots(equipmentSlots); err != nil {
		return fmt.Errorf("attach equipment slot GameData: %w", err)
	}
	equipmentUpgrade, err := gamedata.LoadEquipmentUpgradeDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load equipment upgrade GameData: %w", err)
	}
	if err := ownedEquipment.AttachUpgrade(equipmentUpgrade, wallet, ownedItems); err != nil {
		return fmt.Errorf("attach equipment upgrade GameData: %w", err)
	}
	equipmentCraft, err := gamedata.LoadEquipmentCraftDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load equipment crafting GameData: %w", err)
	}
	talentGrowth, err := gamedata.LoadTalentGrowthDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load talent growth GameData: %w", err)
	}
	if err := ownedEquipment.AttachCraft(equipmentCraft); err != nil {
		return fmt.Errorf("attach equipment crafting GameData: %w", err)
	}
	equipmentSmelting, err := gamedata.LoadEquipmentSmeltingDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load equipment smelting GameData: %w", err)
	}
	if err := ownedEquipment.AttachSmelting(equipmentSmelting, wallet, ownedItems); err != nil {
		return fmt.Errorf("attach equipment smelting GameData: %w", err)
	}
	equipmentOptionReroll, err := gamedata.LoadEquipmentOptionRerollDesign(gameData, *gameDataVersion)
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
	infiniteGacha, err := gamedata.LoadInfiniteGacha(gameData, *gameDataVersion)
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
	if err := login.AttachFirstGacha(gachaService); err != nil {
		return fmt.Errorf("attach first gacha status to login: %w", err)
	}
	if err := gachaService.AttachFirstGacha(firstGacha); err != nil {
		return fmt.Errorf("attach first gacha GameData: %w", err)
	}
	gachaService.AttachInventory(ownedItems)
	gachaService.AttachEquipmentGacha(equipmentGacha, ownedEquipment)
	gachaService.AttachPreviewMission(func() error {
		return missionService.CompleteMission(gamedata.MissionKey{GroupType: 0, GroupID: 1, ID: 111})
	})
	worldService, err := world.Load(filepath.Clean(*worldSeed), gameData, *gameDataVersion,
		stateRepository, progressState, starter, ownedEquipment, ownedItems, wallet)
	if err != nil {
		return fmt.Errorf("load world state: %w", err)
	}
	if err := collection.BindBaseCharacters(worldService.CharacterService().RawAll()); err != nil {
		return fmt.Errorf("bind base collection characters: %w", err)
	}
	if err := mailService.AttachCostumeRewards(collection, limitedCostumes); err != nil {
		return fmt.Errorf("attach limited costume mail rewards: %w", err)
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
	pictorialDesign, err := gamedata.LoadPictorialDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load pictorial GameData: %w", err)
	}
	pictorialService := &pictorial.Service{Design: pictorialDesign, Owned: worldService}
	charAwakeDesign, err := gamedata.LoadCharAwakeDesign(gameData, *gameDataVersion)
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
	costumePotentialDesign, err := gamedata.LoadCostumePotentialDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load costume potential GameData: %w", err)
	}
	costumePotentialService, err := player.NewCostumePotentialService(costumePotentialDesign, collection, worldService.CharacterService(), ownedItems, wallet)
	if err != nil {
		return err
	}
	costumeBurstDesign, err := gamedata.LoadCostumeBurstDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load costume burst GameData: %w", err)
	}
	costumeBurstService, err := player.NewCostumeBurstService(costumeBurstDesign, collection, ownedItems, wallet)
	if err != nil {
		return err
	}
	battleService := battle.NewService(gameData, *gameDataVersion, ownedItems, worldService.CurrentPackID)
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
		costumeBurstService,
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
	if authService != nil {
		if err := game.AttachLoginAuthenticator(authService); err != nil {
			return err
		}
	}
	featured := gacha.ActivePickupCostumes(regularGacha, gachaSchedule, uint64(time.Now().UTC().UnixMilli()))
	limitedIDs := limitedCostumes.Excluding(featured)
	if len(limitedIDs) != 0 {
		if err := mailService.EnsureStarterLimitedCostumes(limitedIDs, time.Now().UTC()); err != nil {
			return fmt.Errorf("ensure account limited-costume entitlement: %w", err)
		}
	}
	if initializeAccount {
		if err := ensureAccountStateInitialized(
			progressState, deckStateStore, ownedItems, ownedEquipment,
			worldService.CharacterService(), collection, wallet, inventorySlots, mailService, missionService,
		); err != nil {
			return fmt.Errorf("initialize complete account state generation: %w", err)
		}
		if err := stateRepository.MarkInitializationComplete(); err != nil {
			return fmt.Errorf("mark account initialization complete: %w", err)
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
	var authHandler http.Handler
	if authService != nil {
		authHandler = authService.Handler()
	}
	availability := lifecycle.NewGate()
	instanceBytes := make([]byte, 16)
	if _, err := rand.Read(instanceBytes); err != nil {
		return fmt.Errorf("create server instance identity: %w", err)
	}
	instanceID := hex.EncodeToString(instanceBytes)
	handler := transport.HTTP{
		Dispatcher: dispatcher, Raw: game, Authentication: authentication,
		AuthenticationHandler: authHandler, ResourcePolicy: publicResources, Availability: availability, InstanceID: instanceID,
	}.Handler()
	server := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	slog.Info("BD2 server listening", "address", *listen, "instance_id", instanceID, "server_version", versions.ServerVersion, "game_version", cfg.Version, "bundle", cfg.BundleVer, "resourceMode", publicResources.Mode, "gameData", verifiedGameData.ArchivePath, "gameDataEntries", verifiedGameData.EntryCount, "accountSeed", *accountSeed)
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.ListenAndServe() }()
	stateFailure := make(chan error, 1)
	stopStateMonitor := make(chan struct{})
	defer close(stopStateMonitor)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := stateRepository.Check(); err != nil {
					select {
					case stateFailure <- err:
					default:
					}
					return
				}
			case <-stopStateMonitor:
				return
			}
		}
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, lifecycle.ShutdownSignals()...)
	defer signal.Stop(signals)
	var shutdownErr error
	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case received := <-signals:
		slog.Info("BD2 server draining", "signal", received.String())
	case err := <-stateFailure:
		shutdownErr = err
		slog.Error("BD2 server state failed closed; draining for process recovery", "error", err)
	}
	availability.Drain()
	drainContext, cancelDrain := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelDrain()
	if err := availability.Wait(drainContext); err != nil {
		slog.Warn("BD2 request drain timed out", "error", err)
	}
	if err := server.Shutdown(drainContext); err != nil {
		_ = server.Close()
		return fmt.Errorf("shutdown drained server: %w", err)
	}
	if err := <-serveResult; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	slog.Info("BD2 server stopped after drain")
	return shutdownErr
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

func resourcesCommand(args []string) error {
	if len(args) == 0 || args[0] != "fetch" {
		return errors.New("resources requires the fetch subcommand")
	}
	fs := flag.NewFlagSet("resources fetch", flag.ContinueOnError)
	versionConfigPath := fs.String("version-config", "", "repository versions.json override")
	output := fs.String("output", "", "resource mirror output directory (required)")
	platform := fs.String("platform", "StandaloneWindows64", "official ServerData platform")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *output == "" {
		return errors.New("resources fetch requires --output")
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
	manifest, err := resourcefetch.Fetch(context.Background(), resourcefetch.Options{
		OutputRoot: *output, Platform: *platform, BundleVersion: versions.BundleVersion,
		GameDataVersion: versions.GameDataVersion,
		Progress:        func(message string) { slog.Info(message) },
	})
	if err != nil {
		return err
	}
	slog.Info("official resource mirror complete", "output", *output, "bundles", manifest.ServerData.Bundles, "bytes", manifest.ServerData.Bytes)
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `bd2server - BrownDust II server

Usage:
	bd2server preflight [--data-dir DIR] [--version-config FILE]
	bd2server serve [--data-dir DIR] [--version-config FILE] [options]
	bd2server resources fetch --output DIR [--version-config FILE]
	bd2server state check [options]

The server binds to loopback by default.`+developmentUsage())
}
