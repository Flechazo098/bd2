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
	"bd2server/internal/server/calendar"
	"bd2server/internal/server/commerce"
	"bd2server/internal/server/deck"
	"bd2server/internal/server/eventactions"
	"bd2server/internal/server/eventexchange"
	"bd2server/internal/server/eventgames"
	"bd2server/internal/server/eventplay"
	"bd2server/internal/server/events"
	"bd2server/internal/server/eventtasks"
	"bd2server/internal/server/feature"
	"bd2server/internal/server/gacha"
	"bd2server/internal/server/gameconfig"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/hunting"
	"bd2server/internal/server/lifecycle"
	"bd2server/internal/server/mail"
	"bd2server/internal/server/missions"
	"bd2server/internal/server/monsterhunt"
	"bd2server/internal/server/npcinn"
	"bd2server/internal/server/npcshop"
	"bd2server/internal/server/pictorial"
	"bd2server/internal/server/player"
	"bd2server/internal/server/progress"
	"bd2server/internal/server/readonly"
	"bd2server/internal/server/resourcefetch"
	"bd2server/internal/server/resourcepolicy"
	"bd2server/internal/server/session"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/todayquest"
	"bd2server/internal/server/transport"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
	"bd2server/internal/server/world"
)

func main() {
	if err := configureLogging(os.Stderr, "", ""); err != nil {
		fmt.Fprintln(os.Stderr, "logging configuration failed:", err)
		os.Exit(2)
	}
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
	stateFile := fs.String("state", "", "account SQLite database override")
	deckSeed := fs.String("deck-seed", "", "versioned starter deck")
	worldSeed := fs.String("world-seed", "", "versioned starter world")
	devToolsConfig := fs.String("dev-tools-config", "", "development-tool settings JSON (defaults to DATA_DIR/dev-tools.json)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := configureLogging(os.Stderr, *logLevel, *logColor); err != nil {
		return err
	}
	defer func() {
		if err := gamedata.CloseDatabaseCache(); err != nil {
			serveErr = errors.Join(serveErr, fmt.Errorf("close GameData query cache: %w", err))
		}
	}()
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
	calendarDirectory := versions.Resolve("schedules")
	calendars, err := calendar.LoadDirectory(calendarDirectory, versions.GameVersion, versions.GameDataVersion)
	if err != nil {
		return fmt.Errorf("load project calendars: %w", err)
	}
	if calendars.RegularService == nil || calendars.MonsterHunt == nil || len(calendars.MonsterHunt.Seasons) == 0 {
		return errors.New("project calendars require regular content and monster hunt schedules")
	}
	slog.Info("project calendars loaded", "directory", calendarDirectory, "revisions", calendars.Revisions, "events", len(calendars.Events))
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
		return err
	}
	verifiedGameData, downloaded, err := gamedata.Ensure(context.Background(), nil, gameData, *gameDataVersion, *gameDataOrigin)
	if err != nil {
		return fmt.Errorf("refuse to advertise unavailable or unverified GameData: %w", err)
	}
	if downloaded {
		slog.Info("repaired GameData from official CDN", "archive", verifiedGameData.ArchivePath, "entries", verifiedGameData.EntryCount)
	}
	if err := calendars.ValidateDesign(gameData, *gameDataVersion); err != nil {
		return fmt.Errorf("validate project calendar GameData references: %w", err)
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
	gachaSchedule := calendars.GachaSeed
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
		"include_collaboration_ur_weapons", gameRules.Gacha.IncludeCollaborationURWeapons,
		"starting_pack_id", gameRules.Story.StartPackID)
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
	startingPackID, err := stateRepository.LockStartingPack(gameRules.Story.StartPackID, initializeAccount)
	if err != nil {
		return fmt.Errorf("server starting chapter policy: %w", err)
	}
	serverConfig, err := readonly.Load(filepath.Clean(*readonlySeed))
	if err != nil {
		return fmt.Errorf("load readonly server configuration: %w", err)
	}
	serverConfig, err = calendars.ApplyReadonly(serverConfig)
	if err != nil {
		return fmt.Errorf("encode project calendars: %w", err)
	}
	seasonSchedule := calendars.RegularService
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
	presetDesign, err := gamedata.LoadPresetDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load party preset GameData: %w", err)
	}
	deckStateStore, err := deck.OpenStore(stateRepository, deckConfig, *presetDesign)
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
	recipeDesign, err := gamedata.LoadCookingRecipeDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load cooking recipes: %w", err)
	}
	recipeService, err := feature.NewRecipeService(recipeDesign, starter.CookingRecipes, ownedItems)
	if err != nil {
		return fmt.Errorf("load learned recipes: %w", err)
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
	contentTickets, err := gamedata.LoadGachaContentTicketDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load gacha content tickets: %w", err)
	}
	if err := mailService.AttachContentTickets(contentTickets); err != nil {
		return fmt.Errorf("attach mailbox content tickets: %w", err)
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
	levelDesign, err := gamedata.LoadAchievementLevelDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load user level rewards: %w", err)
	}
	if err := missionService.AttachUserLevelRewards(levelDesign); err != nil {
		return fmt.Errorf("attach user level rewards: %w", err)
	}
	if err := login.AttachLevelReward(missionService); err != nil {
		return fmt.Errorf("attach persisted user level reward: %w", err)
	}
	if err := missionService.AttachMail(mailService); err != nil {
		return fmt.Errorf("attach mission compensation mailbox: %w", err)
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
	worldService, err := world.Load(filepath.Clean(*worldSeed), gameData, *gameDataVersion,
		stateRepository, progressState, starter, ownedEquipment, ownedItems, wallet)
	if err != nil {
		return fmt.Errorf("load world state: %w", err)
	}
	if err := worldService.ConfigureStartPack(startingPackID, initializeAccount); err != nil {
		return fmt.Errorf("configure account starting chapter: %w", err)
	}
	missionUnlocked := worldService.MissionsUnlocked
	if err := missionService.RecordLogin(missionUnlocked); err != nil {
		return fmt.Errorf("record login missions: %w", err)
	}
	if err := login.AttachLastPlayedPack(worldService); err != nil {
		return fmt.Errorf("attach persisted login destination: %w", err)
	}
	// Restore all earned seed ownership before validating persisted upgrades.
	// A quest costume is not a collection entry; attaching it after opening
	// collection would reject its otherwise valid burst ledger on restart.
	baseCostumes := append([]player.Costume(nil), starter.Costumes...)
	if reward, earned := worldService.EarnedQuestCostume(); earned {
		baseCostumes = append(baseCostumes, reward)
	}
	collection, err := player.OpenCollectionStore(stateRepository, baseCostumes)
	if err != nil {
		return fmt.Errorf("load owned collection: %w", err)
	}
	infiniteGacha, err := gamedata.LoadInfiniteGachaForSchedules(gameData, *gameDataVersion, scheduleGroupIDs)
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
	previewEventIndex, err := serverConfig.CashProductEventIndex(infiniteGacha.ProductGroupID, infiniteGacha.ProductID)
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
	gachaService.AttachDrawMission(func(count uint64) error {
		return missionService.RecordEvent(missions.ConditionGachaBuy, 0, count, missionUnlocked)
	})
	var permanentBaseCharacters []player.Character
	for _, c := range worldService.CharacterService().RawAll() {
		if !player.IsCharmCharacter(c) {
			permanentBaseCharacters = append(permanentBaseCharacters, c)
		}
	}
	if err := collection.BindBaseCharacters(permanentBaseCharacters); err != nil {
		return fmt.Errorf("bind base collection characters: %w", err)
	}
	if err := mailService.AttachCostumeRewards(collection, limitedCostumes); err != nil {
		return fmt.Errorf("attach limited costume mail rewards: %w", err)
	}
	if err := worldService.AttachCollection(collection); err != nil {
		return fmt.Errorf("attach gacha collection state: %w", err)
	}
	if err := worldService.AttachDecks(deckStateStore); err != nil {
		return fmt.Errorf("attach world deck state: %w", err)
	}
	if err := worldService.AttachWaypointRuntime(gameData, *gameDataVersion); err != nil {
		return fmt.Errorf("attach waypoint runtime: %w", err)
	}
	if err := worldService.AttachFieldObjectRuntime(gameData, *gameDataVersion); err != nil {
		return fmt.Errorf("attach field object runtime: %w", err)
	}
	if err := ownedEquipment.AttachCharacters(worldService.CharacterService()); err != nil {
		return fmt.Errorf("attach equipment character state: %w", err)
	}
	if err := deckStateStore.AttachPresetRuntime(wallet, worldService.CharacterService(), ownedEquipment, collection); err != nil {
		return fmt.Errorf("attach ordinary preset runtime: %w", err)
	}
	fieldSettingsDesign, err := gamedata.LoadFieldSettingsDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load field character settings: %w", err)
	}
	if err := deckStateStore.AttachFieldSettingsPack(worldService.CurrentPackID); err != nil {
		return err
	}
	if err := deckStateStore.AttachFieldSettings(fieldSettingsDesign); err != nil {
		return fmt.Errorf("attach field character settings: %w", err)
	}
	if err := login.AttachAutoReviveSettings(deckStateStore); err != nil {
		return fmt.Errorf("attach automatic revival settings: %w", err)
	}
	pictorialDesign, err := gamedata.LoadPictorialDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load pictorial GameData: %w", err)
	}
	pictorialService := &pictorial.Service{Design: pictorialDesign, Owned: worldService}
	equipmentStatDesign, err := gamedata.LoadEquipmentStatDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load equipment stat GameData: %w", err)
	}
	if err := ownedEquipment.AttachStatDesign(equipmentStatDesign); err != nil {
		return err
	}
	pictorialService.EquipmentContributions = ownedEquipment.StatContributions
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
	immortalDesign, err := gamedata.LoadImmortalDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load immortal talent GameData: %w", err)
	}
	if err := worldService.CharacterService().AttachImmortalDesign(immortalDesign); err != nil {
		return fmt.Errorf("attach immortal talent GameData: %w", err)
	}
	costumePotentialDesign, err := gamedata.LoadCostumePotentialDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load costume potential GameData: %w", err)
	}
	costumePotentialService, err := player.NewCostumePotentialService(costumePotentialDesign, collection, worldService.CharacterService(), ownedItems, wallet)
	if err != nil {
		return err
	}
	pictorialService.PotentialContributions = costumePotentialService.Contributions
	costumeBurstDesign, err := gamedata.LoadCostumeBurstDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load costume burst GameData: %w", err)
	}
	costumeBurstService, err := player.NewCostumeBurstService(costumeBurstDesign, collection, ownedItems, wallet)
	if err != nil {
		return err
	}
	friendshipDesign, err := gamedata.LoadFriendshipDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load friendship GameData: %w", err)
	}
	friendshipService, err := player.NewFriendshipService(&friendshipDesign, charAwakeDesign, costumePotentialDesign, collection, ownedItems, wallet)
	if err != nil {
		return fmt.Errorf("load friendship state: %w", err)
	}
	if err := login.AttachFriendshipAP(friendshipService); err != nil {
		return err
	}
	accountName, found, err := wire.Bytes(login.UserInfo, 2)
	if err != nil || !found || len(accountName) == 0 {
		return errors.New("account seed requires its existing display name for master title")
	}
	masterTitleService, err := player.OpenMasterTitleService(stateRepository, string(accountName))
	if err != nil {
		return fmt.Errorf("load master title: %w", err)
	}
	battleService := battle.NewService(gameData, *gameDataVersion, ownedItems, worldService.CurrentPackID)
	freeHuntingAP, bonusHuntingAP, err := login.SeedHuntingAP()
	if err != nil {
		return fmt.Errorf("read initial hunting AP: %w", err)
	}
	gameplayStore := stateio.EntrySnapshotStore{Entries: stateRepository, Domain: "missions", Bucket: "gameplay"}
	if err := worldService.AttachFieldMonsterState(gameplayStore); err != nil {
		return fmt.Errorf("attach field monster state: %w", err)
	}
	battleService.AttachFieldMonsters(worldService)
	if err := worldService.AttachFieldBuffRuntime(gameData, *gameDataVersion); err != nil {
		return fmt.Errorf("attach field monster damage: %w", err)
	}
	contentOpeningDesign, err := gamedata.LoadContentOpeningDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load content opening GameData: %w", err)
	}
	contentOpenService, err := player.NewContentOpenService(contentOpeningDesign, ownedItems, gameplayStore, func() (uint64, error) {
		experience, err := missionService.AchievementExperience()
		if err != nil {
			return 0, err
		}
		return levelDesign.Level(experience), nil
	})
	if err != nil {
		return fmt.Errorf("load content opening state: %w", err)
	}
	huntingService, err := hunting.Open(gameplayStore, gameData, *gameDataVersion, ownedItems, wallet,
		worldService.CurrentPackID, freeHuntingAP, bonusHuntingAP)
	if err != nil {
		return fmt.Errorf("load hunting state: %w", err)
	}
	if err := login.AttachHuntingAP(huntingService); err != nil {
		return fmt.Errorf("attach persisted hunting AP: %w", err)
	}
	huntingAPDesign, err := gamedata.LoadHuntingAPDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load hunting AP reset: %w", err)
	}
	if err = huntingService.AttachAPRefresh(huntingAPDesign); err != nil {
		return err
	}
	battleService.AttachHunting(huntingService)
	huntingService.AttachEligibility(worldService.HuntingEligibility)
	if err := worldService.AttachHuntingGround(huntingService); err != nil {
		return err
	}
	eventRegistry := events.NewRegistry()
	if err := eventRegistry.Replace(calendars.Events); err != nil {
		return err
	}
	rewardGraph, err := gamedata.LoadRewardGraph(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load event reward graph: %w", err)
	}
	rewardEquipment, err := gamedata.LoadRewardEquipmentCatalog(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load reward equipment: %w", err)
	}
	rewardCostumes, err := gamedata.LoadRewardCostumeCatalog(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load reward costumes: %w", err)
	}
	initialEventCurrency := map[uint64]uint64{}
	for itemType, field := range events.AdditionalCurrencyFields {
		value, _, readErr := wire.Varint(login.UserInfo, field)
		if readErr != nil {
			return readErr
		}
		initialEventCurrency[itemType] = value
	}
	eventEconomy, err := events.NewEconomy(gameplayStore, ownedItems, wallet, collection, ownedEquipment, rewardCostumes, rewardEquipment, rewardGraph, initialEventCurrency)
	if err != nil {
		return fmt.Errorf("load event economy: %w", err)
	}
	eventEconomy.AttachHuntingAP(huntingService)
	talentUseDesign, err := gamedata.LoadTalentUseDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load field talent skills: %w", err)
	}
	talentUseService, err := player.NewTalentUseService(talentUseDesign, gameplayStore, worldService.CharacterService(), ownedItems, wallet, eventEconomy)
	if err != nil {
		return fmt.Errorf("load field talent state: %w", err)
	}
	talentUseService.AttachContext(worldService.TalentFieldContext)
	if err := worldService.AttachAutoRecoveryPolicy(gameData, *gameDataVersion); err != nil {
		return fmt.Errorf("attach automatic recovery policy: %w", err)
	}
	deckStateStore.AttachAutoRecoveryAllowed(worldService.AutoRecoveryAllowed)
	deckStateStore.AttachAutoRecovery(talentUseService.AutoRecover)
	worldService.AttachTalentPackInfo(talentUseService.PackInfo)
	worldService.AttachOverwhelmAuthorization(talentUseService.ConsumeOverwhelm)
	worldService.AttachOverwhelmHunting(huntingService)
	if err := worldService.AttachOverwhelmDesign(gameData, *gameDataVersion); err != nil {
		return fmt.Errorf("attach overwhelm design: %w", err)
	}
	talentUseService.AttachEffect(4, worldService.ApplyTalentFieldAbsorb)
	talentUseService.AttachEffect(20, worldService.ApplyTalentMonsterSummon)
	dispatchDesign, err := gamedata.LoadTalentDispatchDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load talent dispatch design: %w", err)
	}
	dispatchService, err := player.OpenTalentDispatch(gameplayStore, dispatchDesign, eventEconomy)
	if err != nil {
		return fmt.Errorf("load talent dispatch state: %w", err)
	}
	talentUseService.AttachEffect(18, dispatchService.Start)
	itemCraftDesign, err := gamedata.LoadItemCraftDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load item crafting design: %w", err)
	}
	itemCraftService, err := player.NewItemCraftService(itemCraftDesign, talentUseDesign, gameplayStore, ownedItems, worldService.CharacterService(), wallet, recipeService.Knows)
	if err != nil {
		return fmt.Errorf("load item crafting state: %w", err)
	}
	itemCraftService.AttachContext(func() (int, bool, error) {
		pack, err := worldService.CurrentPackID()
		return pack, battleService.Active(), err
	})
	if err := worldService.ConfigureNPCRuntime(gameData, *gameDataVersion, gameplayStore); err != nil {
		return fmt.Errorf("configure NPC world runtime: %w", err)
	}
	innService, err := npcinn.New(gameplayStore, worldService.CharacterService(), wallet, worldService.InnContext,
		func() (uint64, error) {
			experience, err := missionService.AchievementExperience()
			if err != nil {
				return 0, err
			}
			return levelDesign.Level(experience), nil
		}, battleService.Active)
	if err != nil {
		return fmt.Errorf("load inn recovery: %w", err)
	}
	npcShopDesign, err := gamedata.LoadNPCShopDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load NPC shop design: %w", err)
	}
	npcShopService, err := npcshop.New(npcShopDesign, gameplayStore, eventEconomy, ownedItems, worldService.PackAvailable)
	if err != nil {
		return fmt.Errorf("load NPC shop state: %w", err)
	}
	npcShopService.SetReputationSource(worldService.NPCShopReputation)
	npcShopService.SetTalentDiscountSource(talentUseService.ShopDiscount)
	commissionDesign, err := gamedata.LoadTodayQuests(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load NPC commission design: %w", err)
	}
	commissionService, err := todayquest.Open(gameplayStore, commissionDesign, eventEconomy, ownedItems, worldService.CommissionPackUnlocked)
	if err != nil {
		return fmt.Errorf("load NPC commission state: %w", err)
	}
	commissionService.CompleteReputation = worldService.CompleteNPCReputation
	if err := worldService.AttachTodayQuests(commissionService); err != nil {
		return fmt.Errorf("attach NPC commissions: %w", err)
	}
	if err = worldService.AttachResearchRuntime(gameData, *gameDataVersion, eventEconomy); err != nil {
		return fmt.Errorf("attach field research: %w", err)
	}
	prestigeCatalog, err := gamedata.LoadPrestigeSkinCatalog(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load reward prestige skins: %w", err)
	}
	eventEconomy.AttachPrestigeSkins(prestigeCatalog.Skins)
	eventEconomy.AttachPrestigePortrait(deckStateStore.PortraitCostume)
	if err := worldService.AttachPrestigeSelections(eventEconomy.PrestigeSkinSelections); err != nil {
		return fmt.Errorf("attach prestige skin selections: %w", err)
	}
	ownedEventItems, err := gamedata.LoadOwnedEventItemDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load event inventory design: %w", err)
	}
	eventEconomy.AttachOwnedItemDesign(ownedEventItems)
	avatarRewards, err := gamedata.LoadAvatarRewardDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load avatar rewards: %w", err)
	}
	eventEconomy.AttachAvatarRewards(avatarRewards)
	buffDesign, err := gamedata.LoadBuffRewardDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load permanent buff rewards: %w", err)
	}
	buffRewards, err := events.OpenBuffRewards(gameplayStore, buffDesign)
	if err != nil {
		return fmt.Errorf("load permanent buff ownership: %w", err)
	}
	eventEconomy.AttachBuffRewards(buffRewards)
	pictorialService.AttachPermanentBuffs(buffRewards.SnapshotBuffs)
	eventAPCaps, eventAPReset, err := gamedata.LoadEventAPDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load event AP reset: %w", err)
	}
	if err = eventEconomy.AttachAPRefresh(eventAPCaps, eventAPReset); err != nil {
		return err
	}
	if err = login.AttachAdditionalCurrencies(eventEconomy); err != nil {
		return err
	}
	huntingService.AttachRewards(func(identity string, rewards []gamedata.Reward) ([]byte, error) {
		return eventEconomy.Apply(identity, nil, rewards)
	})
	cashDesign, err := gamedata.LoadCashCatalog(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load cash products: %w", err)
	}
	cashCatalog, err := commerce.NewCatalog(versions.GameVersion, cashDesign, gameRules.Purchases)
	if err != nil {
		return fmt.Errorf("configure cash products: %w", err)
	}
	cashEntitlementDesign, err := gamedata.LoadCashEntitlementDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load cash entitlement design: %w", err)
	}
	cashRewards, err := gamedata.LoadCashRewardResolver(gameData, *gameDataVersion, rewardGraph)
	if err != nil {
		return fmt.Errorf("load cash product rewards: %w", err)
	}
	cashEconomy, err := commerce.NewEntitlementEconomy(gameplayStore, eventEconomy, cashRewards, ownedItems, cashEntitlementDesign)
	if err != nil {
		return fmt.Errorf("load cash entitlements: %w", err)
	}
	cashEconomy.SetClock(time.Now, eventAPReset.ResetSeconds-9*3600)
	cashMailTemplates, err := gamedata.LoadCashMailTemplates(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load cash mail templates: %w", err)
	}
	if err := mailService.AttachCashRewards(cashEconomy, cashMailTemplates); err != nil {
		return err
	}
	if err := cashEconomy.AttachCashMail(mailService); err != nil {
		return err
	}
	cashService, err := commerce.NewService(cashCatalog, gameplayStore, cashEconomy)
	if err != nil {
		return fmt.Errorf("load cash purchase state: %w", err)
	}
	clearPackageDesign, err := gamedata.LoadClearPackageCatalog(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load clear-package rewards: %w", err)
	}
	clearPackages, err := commerce.NewClearPackages(gameplayStore, clearPackageDesign, cashEconomy, ownedItems)
	if err != nil {
		return fmt.Errorf("load clear-package claims: %w", err)
	}
	clearPackages.AttachProgress(worldService.CashPackagePackCleared, nil)
	cashService.SetClock(time.Now, eventAPReset.ResetSeconds-9*3600)
	if err := cashService.AttachPackageRules(cashDesign.Packages); err != nil {
		return fmt.Errorf("attach cash package progression: %w", err)
	}
	if err := cashService.AttachShopSeed(serverConfig); err != nil {
		return fmt.Errorf("attach cash product availability: %w", err)
	}
	if err := cashService.AttachEventShopSchedules(cashDesign, calendars.Events); err != nil {
		return fmt.Errorf("attach event shop availability: %w", err)
	}
	cashBonusDesign, err := gamedata.LoadCashBonusCatalog(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load cash bonus design: %w", err)
	}
	cashBonuses, err := commerce.NewCashBonuses(gameplayStore, cashEconomy, cashService, cashBonusDesign, cashDesign.Packages)
	if err != nil {
		return fmt.Errorf("load cash bonus claims: %w", err)
	}
	cashService.AttachLegacyCounts(gachaService)
	cashSpecialProducts := []gamedata.CashProductKey{{GroupID: infiniteGacha.ProductGroupID, ProductID: infiniteGacha.ProductID, SaleGroup: infiniteGacha.SaleGroup}}
	for _, group := range regularGacha.Groups() {
		if group.CashProductGroupID != 0 && group.CashProductID != 0 {
			cashSpecialProducts = append(cashSpecialProducts, gamedata.CashProductKey{GroupID: group.CashProductGroupID, ProductID: group.CashProductID, SaleGroup: group.CashSalesGroup})
		}
	}
	if err := cashService.AttachSpecialProducts(cashSpecialProducts); err != nil {
		return fmt.Errorf("attach special cash products: %w", err)
	}
	cashService.AttachDelegate(func(key gamedata.CashProductKey, request []byte) ([]byte, bool, error) {
		known := key.GroupID == infiniteGacha.ProductGroupID && key.ProductID == infiniteGacha.ProductID && key.SaleGroup == infiniteGacha.SaleGroup
		for _, group := range regularGacha.Groups() {
			if key.GroupID == group.CashProductGroupID && key.ProductID == group.CashProductID && key.SaleGroup == group.CashSalesGroup {
				known = true
				break
			}
		}
		if !known {
			return nil, false, nil
		}
		_, response, handled, err := gachaService.Handle("/CashShopBuy", request)
		if err != nil || !handled {
			return nil, handled, err
		}
		bundle, _, err := wire.Bytes(response, 1)
		return bundle, true, err
	})
	if err := login.AttachPurchaseCounts(cashService); err != nil {
		return fmt.Errorf("attach cash purchase counts: %w", err)
	}
	eventTasksDesign, err := gamedata.LoadEventTasksDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load event tasks design: %w", err)
	}
	eventTasksService, err := eventtasks.Open(gameplayStore, eventTasksDesign, eventRegistry, eventEconomy)
	if err != nil {
		return fmt.Errorf("load event tasks state: %w", err)
	}
	if err := mailService.AttachAttendanceRewardEconomy(eventEconomy); err != nil {
		return fmt.Errorf("attach attendance mail rewards: %w", err)
	}
	eventTasksService.AttachAttendanceMail(mailService)
	newbieStep, _, err := wire.Varint(login.UserInfo, 39)
	if err != nil {
		return err
	}
	if err = eventTasksService.SetNewbieStep(newbieStep); err != nil {
		return err
	}
	if err = login.AttachNewbieStep(eventTasksService); err != nil {
		return err
	}
	eventTasksService.AttachCashAuthorization(func(passID, buyType uint64) bool {
		for _, buy := range eventTasksDesign.PassBuys[passID] {
			if buy.Type == buyType && buy.CashID != 0 {
				return cashService.ConsumeEntitlement(gamedata.CashProductKey{GroupID: buy.CashGroup, ProductID: buy.CashID, SaleGroup: buy.CashSales})
			}
		}
		return false
	})
	eventTasksService.AttachAttendancePremium(func(ticket uint64) bool {
		for _, item := range ownedItems.All() {
			if item.Type == 19 && item.ID == ticket && item.Count > 0 && (item.ExpiryTime == 0 || item.ExpiryTime > uint64(time.Now().UnixMilli())) {
				return true
			}
		}
		return false
	})
	loginPassDesign, err := gamedata.LoadLoginPassCatalog(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load login-pass rewards: %w", err)
	}
	loginPasses, err := commerce.NewLoginPasses(gameplayStore, loginPassDesign, cashEconomy, ownedItems, func(group uint64) bool {
		for _, pack := range cashDesign.Packages {
			if pack.PackageType == 7 && pack.ID == group && cashService.IsAvailable(gamedata.CashProductKey{GroupID: pack.GroupID, ProductID: pack.ID, SaleGroup: pack.SaleGroup}) {
				return true
			}
		}
		return false
	})
	if err != nil {
		return fmt.Errorf("load login-pass progress: %w", err)
	}
	loginPasses.SetClock(time.Now, eventAPReset.ResetSeconds-9*3600)
	eventTasksService.AttachUnlockResolver(missionUnlocked)
	missionService.AttachEventHandler(eventTasksService)
	if err = missionService.RecordLogin(missionUnlocked); err != nil {
		return err
	}
	eventGamesService, err := eventgames.Open(gameplayStore, gameData, *gameDataVersion, eventRegistry, eventEconomy)
	if err != nil {
		return fmt.Errorf("load event games state: %w", err)
	}
	eventExchangeDesign, err := gamedata.LoadEventExchangeCatalog(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load event exchange design: %w", err)
	}
	eventExchangeService, err := eventexchange.Open(gameplayStore, eventExchangeDesign, eventRegistry, eventEconomy)
	if err != nil {
		return fmt.Errorf("load event exchange state: %w", err)
	}
	boxService, err := events.OpenBoxes(gameplayStore, ownedItems, eventEconomy)
	if err != nil {
		return fmt.Errorf("load random box state: %w", err)
	}
	eventPlayService, err := eventplay.Open(gameplayStore, gameData, *gameDataVersion, eventRegistry, eventEconomy)
	if err != nil {
		return fmt.Errorf("load event play state: %w", err)
	}
	eventBattleChallenges, err := gamedata.LoadEventBattleChallenges(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load event battle challenges: %w", err)
	}
	eventPlayService.AttachBattleChallenges(eventBattleChallenges)
	eventPlayService.AttachHubCalendars(serverConfig)
	if err := eventPlayService.AttachFieldBindingsFile(filepath.Join(filepath.Dir(*worldSeed), "event_field_bindings.json")); err != nil {
		return fmt.Errorf("attach hidden field bindings: %w", err)
	}
	if err := worldService.AttachEventFieldPacks(eventPlayService); err != nil {
		return fmt.Errorf("attach event field packs: %w", err)
	}
	battleService.AttachEventBattle(eventPlayService)
	eventActionsDesign, err := gamedata.LoadEventActionsDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load event action design: %w", err)
	}
	eventActionsService, err := eventactions.Open(gameplayStore, eventActionsDesign, eventRegistry, eventEconomy)
	if err != nil {
		return fmt.Errorf("load event action state: %w", err)
	}
	miniContent, err := gamedata.LoadMiniContentDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load mini event content: %w", err)
	}
	if err = eventActionsService.AttachMiniContent(eventPlayService, miniContent); err != nil {
		return fmt.Errorf("attach mini event content: %w", err)
	}
	eventActionsService.AttachFriendshipLevel(func(id uint64) uint64 {
		for _, entry := range collection.FriendshipEntries() {
			if entry.State != nil && entry.State.CostumeID == id {
				return entry.State.Level
			}
		}
		return 0
	})
	eventActionsService.AttachOwnedCharacter(func(index, id uint64) bool {
		c, ok := worldService.CharacterService().Find(index)
		return ok && c.ID == id
	})
	eventActionsService.AttachChargeInfo(func() ([]byte, error) {
		rows, err := eventEconomy.ChargeInfo()
		if err != nil {
			return nil, err
		}
		huntingRows, err := huntingService.APChargeInfo()
		if err != nil {
			return nil, err
		}
		return append(rows, huntingRows...), nil
	})
	eventActionsService.AttachProgress(func(condition, sub, count uint64) error {
		if err := missionService.RecordEvent(condition, sub, count, missionUnlocked); err != nil {
			return err
		}
		return eventTasksService.RecordEvent(condition, sub, count, missionUnlocked)
	})
	eventPlayService.AttachProgress(func(condition, sub, count uint64) error {
		return missionService.RecordEvent(condition, sub, count, missionUnlocked)
	})
	eventTasksService.AttachAssociatedMissionGroup(func(schedule events.Schedule) uint64 {
		if group := eventActionsService.AssociatedMissionGroup(schedule); group != 0 {
			return group
		}
		group, err := eventPlayService.AssociatedMissionGroup(schedule)
		if err != nil {
			slog.Error("event mission design unavailable", "event_uid", schedule.UID, "event_id", schedule.ID, "error", err)
		}
		return group
	})
	battleService.AttachEventBattle(eventActionsService)
	battleService.AttachCurrentDifficulty(worldService.CurrentQuestDifficulty)
	if err := worldService.AttachBattleActive(battleService.Active); err != nil {
		return fmt.Errorf("attach world battle guard: %w", err)
	}
	characters := worldService.CharacterService()
	monsterHuntService, err := monsterhunt.Open(gameplayStore, gameData, *gameDataVersion, serverConfig, ownedItems, wallet)
	if err != nil {
		return fmt.Errorf("load monster hunt state: %w", err)
	}
	if err := monsterHuntService.AttachPresetRuntime(characters, ownedEquipment, collection); err != nil {
		return fmt.Errorf("attach monster hunt preset ownership: %w", err)
	}
	if err := login.AttachMonsterHuntSlots(monsterHuntService); err != nil {
		return fmt.Errorf("attach monster hunt preset slots: %w", err)
	}
	battleService.AttachMonsterHunt(monsterHuntService)
	monsterHuntService.AttachRewards(func(identity string, rewards []gamedata.Reward) ([]byte, error) {
		return eventEconomy.Apply(identity, nil, rewards)
	})
	recruitDesign, err := gamedata.LoadRecruitDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load recruitment GameData: %w", err)
	}
	recruitService, err := player.NewRecruitService(&recruitDesign, &recruitDesign, collection, ownedItems, wallet,
		func(npcID uint64) (uint64, error) {
			return worldService.ResolveRecruitNPC(npcID, gameData, *gameDataVersion, &recruitDesign)
		})
	if err != nil {
		return fmt.Errorf("load recruitment service: %w", err)
	}
	battleService.AttachCommittedHealth(func(health map[uint64]uint64) error {
		for index, hp := range health {
			maximum, err := characters.MaxHealth(index)
			if err != nil {
				return fmt.Errorf("invalid completed battle health for character %d: %w", index, err)
			}
			if hp > maximum {
				// Battle-only HP buffs are not persisted into field health.
				// This is our settlement policy, not an inferred provider rule.
				health[index] = maximum
			}
		}
		for index, hp := range health {
			if err := characters.SetCurrentHealth(index, hp); err != nil {
				return err
			}
		}
		return nil
	})
	foodDesign, err := gamedata.LoadFoodDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load food GameData: %w", err)
	}
	foodService, err := player.OpenFoodService(stateRepository, foodDesign, ownedItems, characters)
	if err != nil {
		return fmt.Errorf("load food state: %w", err)
	}
	if err := foodService.AttachContext(worldService.CurrentPackID, battleService.Active); err != nil {
		return err
	}
	battleService.AttachMonsterWinMission(func() error {
		return missionService.CompleteSingleTargetEvent(missions.ConditionMonsterKill, missionUnlocked)
	})
	battleService.AttachPictorialBuffs(func() ([]gamedata.PictorialBuffStat, error) {
		_, buffs, err := pictorialService.Snapshot()
		return buffs, err
	})
	achievementCounterDesign, err := gamedata.LoadAchievementCounterDesign(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load achievement counters: %w", err)
	}
	achievementCounters, err := world.NewAchievementService(achievementCounterDesign, stateRepository, missionService)
	if err != nil {
		return fmt.Errorf("load achievement counter state: %w", err)
	}
	if err := missionService.AttachAchievementProgress(achievementCounters); err != nil {
		return fmt.Errorf("attach achievement completion validation: %w", err)
	}
	commissionService.CompleteAchievement = func(identity string) error {
		_, err := achievementCounters.RecordEvent(identity, 17, 0, 1)
		return err
	}
	if err := login.AttachAchievementExperience(missionService); err != nil {
		return fmt.Errorf("attach persisted achievement experience: %w", err)
	}
	achievementGrades, err := gamedata.LoadGameplayAchievementGrades(gameData, *gameDataVersion)
	if err != nil {
		return fmt.Errorf("load achievement gameplay grades: %w", err)
	}
	achievementObserver, err := world.NewGameplayAchievementObserver(achievementCounters,
		worldService.GameplayAchievementProvider(achievementCounterDesign, achievementGrades))
	if err != nil {
		return fmt.Errorf("initialize achievement gameplay observer: %w", err)
	}
	if err := achievementObserver.SyncRecordedHistory(); err != nil {
		return fmt.Errorf("restore recorded achievement history: %w", err)
	}
	eventTasksService.AttachGameplayProvider(worldService.GameplayAchievementProvider(achievementCounterDesign, achievementGrades))
	game, err := session.NewServerWithProgress(login, progressState,
		cashService,
		cashBonuses,
		clearPackages,
		commerce.PackInfoHandler{World: worldService, Claims: clearPackages},
		commerce.AttendanceHandler{Events: eventTasksService, Economy: cashEconomy, LoginPasses: loginPasses, Store: gameplayStore},
		eventRegistry,
		eventGamesService,
		eventExchangeService,
		boxService,
		eventPlayService,
		eventActionsService,
		npcShopService,
		innService,
		events.SkinHandler{Economy: eventEconomy},
		battleService,
		huntingService,
		monsterHuntService,
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
		friendshipService,
		contentOpenService,
		masterTitleService,
		recruitService,
		foodService,
		talentUseService,
		dispatchService,
		itemCraftService,
		recipeService,
		starter,
		mailService,
		gachaService,
		achievementCounters,
		missionService,
		eventTasksService,
		pictorialService,
		seasonSchedule,
		readonly.Service{Seed: serverConfig},
		feature.Service{},
	)
	if err != nil {
		return err
	}
	if err := game.AttachResponseObserver(achievementObserver); err != nil {
		return fmt.Errorf("attach achievement progress notifications: %w", err)
	}
	if err := game.AttachResponseObserver(eventTasksService); err != nil {
		return err
	}
	if err := game.AttachResponseObserver(mailService); err != nil {
		return fmt.Errorf("attach new mail notifications: %w", err)
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
	prestigeIDs := prestigeCatalog.Giftable(cashService.IsAvailable)
	if len(prestigeIDs) != 0 {
		if err := mailService.EnsureStarterPrestigeSkins(prestigeIDs, time.Now().UTC()); err != nil {
			return fmt.Errorf("ensure account prestige-skin entitlement: %w", err)
		}
	}
	if initializeAccount {
		if err := ensureAccountStateInitialized(
			progressState, deckStateStore, ownedItems, ownedEquipment,
			worldService.CharacterService(), collection, wallet, inventorySlots, mailService, missionService,
		); err != nil {
			return fmt.Errorf("initialize complete account state generation: %w", err)
		}
		if err := worldService.EnsureInitialPackPurchase(); err != nil {
			return fmt.Errorf("grant initial pack purchase rewards: %w", err)
		}
		if err := stateRepository.MarkInitializationComplete(); err != nil {
			return fmt.Errorf("mark account initialization complete: %w", err)
		}
	}
	if err := masterTitleService.EnsurePersisted(); err != nil {
		return fmt.Errorf("persist master title: %w", err)
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
		AuthenticationHandler: authHandler, ResourcePolicy: publicResources,
		CommerceManifest: func() any { return cashCatalog.Manifest() }, Availability: availability, InstanceID: instanceID,
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

The server binds to loopback by default.
Logging: --log-level trace|debug|info|warn|error; --log-color auto|always|never.
BD2_LOG_LEVEL and BD2_LOG_COLOR set defaults for all commands.`+developmentUsage())
}
