package app

import (
	"bd2server/internal/server/domain/battle"
	"bd2server/internal/server/domain/battle/hunting"
	"bd2server/internal/server/domain/battle/monsterhunt"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/commerce"
	"bd2server/internal/server/domain/commerce/gacha"
	"bd2server/internal/server/domain/commerce/npcinn"
	"bd2server/internal/server/domain/commerce/npcshop"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/domain/events/actions"
	"bd2server/internal/server/domain/events/exchange"
	"bd2server/internal/server/domain/events/games"
	"bd2server/internal/server/domain/events/play"
	"bd2server/internal/server/domain/events/tasks"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/mail"
	"bd2server/internal/server/domain/progression/achievements"
	"bd2server/internal/server/domain/progression/missions"
	"bd2server/internal/server/domain/progression/pictorial"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/roster/deck"
	"bd2server/internal/server/domain/world"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/domain/world/todayquest"
	"bd2server/internal/server/gateway/session"
	loginprotocol "bd2server/internal/server/protocol/login"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/account"
	identitystore "bd2server/internal/server/storage/identity"
	"bd2server/internal/server/storage/stateio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

type PlayerFactory struct {
	options  *configuration
	design   *designCatalog
	seeds    *seedCatalog
	profiles interface {
		GameIdentity(context.Context, string) (identitystore.GameProfile, error)
	}
}

type playerAssembly struct {
	worldSeed world.Seed
	mailbox   *mail.Starter
	deckSeed  deck.Seed
	*PlayerFactory
	progressState           *progress.Store
	deckStateStore          *deck.Store
	ownedItems              *assets.Inventory
	recipeService           *assets.RecipeService
	wallet                  *assets.Wallet
	inventorySlots          *assets.InventorySlots
	mailService             *mail.Service
	missionService          *missions.Service
	ownedEquipment          *assets.EquipmentInventory
	worldService            *world.Service
	collection              *roster.CollectionStore
	gachaService            *gacha.Service
	pictorialService        *pictorial.Service
	charAwakeService        *roster.CharAwakeService
	costumePotentialService *roster.CostumePotentialService
	costumeBurstService     *roster.CostumeBurstService
	friendshipService       *roster.FriendshipService
	masterTitleService      *roster.MasterTitleService
	battleService           *battle.Service
	gameplayStore           stateio.EntrySnapshotStore
	contentOpenService      *assets.ContentOpenService
	huntingService          *hunting.Service
	eventRegistry           *events.Registry
	eventEconomy            *events.Economy
	talentUseService        *roster.TalentUseService
	dispatchService         *roster.TalentDispatchService
	itemCraftService        *roster.ItemCraftService
	innService              *npcinn.Service
	npcShopService          *npcshop.Service
	commissionService       *todayquest.Service
	buffRewards             *events.BuffRewards
	cashCatalog             *commerce.Catalog
	cashEconomy             *commerce.EntitlementEconomy
	cashService             *commerce.Service
	clearPackages           *commerce.ClearPackages
	cashBonuses             *commerce.CashBonuses
	eventTasksService       *eventtasks.Service
	loginPasses             *commerce.LoginPasses
	eventGamesService       *eventgames.Service
	eventExchangeService    *eventexchange.Service
	boxService              *events.BoxService
	eventPlayService        *eventplay.Service
	eventActionsService     *eventactions.Service
	monsterHuntService      *monsterhunt.Service
	recruitService          *roster.RecruitService
	foodService             *roster.FoodService
	achievementCounters     *achievements.AchievementService
	achievementObserver     *achievements.GameplayAchievementObserver
	handlers                []session.Handler
	observers               []session.ResponseObserver
	stateRepository         *accountstate.Repository
	scope                   stateio.RootStore
	startingPackID          int
	initializeAccount       bool
	login                   *loginprotocol.LoginSeed
	starter                 *roster.Starter
}

type playerInstance struct {
	accountID  string
	factory    *PlayerFactory
	repository *accountstate.Repository
	assembly   *playerAssembly
}

func (f *PlayerFactory) open(accountID string) (instance *playerInstance, openErr error) {
	statePath := filepath.Join(f.options.stateDirectory, "accounts", accountDirectoryName(accountID), "state.db")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		return nil, err
	}
	repository, err := accountstate.Open(statePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			openErr = fmt.Errorf("initialize player panic: %v", recovered)
			instance = nil
		}
		if openErr != nil {
			openErr = errors.Join(openErr, repository.Close())
		}
	}()
	assembly, err := f.assemble(context.Background(), accountID, repository)
	if err != nil {
		return nil, err
	}
	return &playerInstance{accountID: accountID, factory: f, repository: repository, assembly: assembly}, nil
}

func (f *PlayerFactory) assemble(ctx context.Context, accountID string, repository *accountstate.Repository) (_ *playerAssembly, openErr error) {

	login := &loginprotocol.LoginSeed{Version: f.seeds.login.Version, PacketCode: f.seeds.login.PacketCode,
		UserInfo: slices.Clone(f.seeds.login.UserInfo), ResponseFields: slices.Clone(f.seeds.login.ResponseFields)}
	if f.profiles != nil {
		profile, err := f.profiles.GameIdentity(ctx, accountID)
		if err != nil {
			return nil, err
		}
		login.UserInfo, _, err = wire.ReplaceVarint(login.UserInfo, 1, uint64(profile.OwnerIndex))
		if err != nil {
			return nil, err
		}
		login.UserInfo, _, err = wire.ReplaceBytes(login.UserInfo, 2, []byte(profile.UserID))
		if err != nil {
			return nil, err
		}
	}
	starter, err := cloneSeed(f.seeds.starter)
	if err != nil {
		return nil, err
	}
	mailbox, err := cloneSeed(f.seeds.mailbox)
	if err != nil {
		return nil, err
	}
	deckSeed, err := cloneSeed(&f.seeds.deck)
	if err != nil {
		return nil, err
	}
	worldSeed, err := cloneSeed(&f.seeds.world)
	if err != nil {
		return nil, err
	}
	p := &playerAssembly{PlayerFactory: f, stateRepository: repository, login: login, starter: starter, mailbox: mailbox, deckSeed: *deckSeed, worldSeed: *worldSeed}
	accountDomains := []string{"characters", "collection", "deck", "equipment", "items", "mail", "missions", "progress", "wallet"}
	initializationState, err := p.stateRepository.InitializationState(accountDomains...)
	if err != nil {
		return nil, fmt.Errorf("reject incomplete account database: %w", err)
	}
	if initializationState == accountstate.InitializationCorrupt {
		return nil, errors.New("reject incomplete account database: corrupt initialization state")
	}
	p.initializeAccount = initializationState == accountstate.InitializationPending
	startupTransaction, err := p.stateRepository.BeginCommand(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin startup state transaction: %w", err)
	}
	startupCommitted := false
	defer func() {
		if startupCommitted {
			return
		}
		if rollbackErr := startupTransaction.Rollback(); rollbackErr != nil {
			openErr = errors.Join(openErr, rollbackErr)
		}
	}()

	identity := command.Context{Identity: command.Identity{AccountID: accountID, SessionID: "startup", RequestID: "initialize"}, Cancellation: ctx, State: startupTransaction}
	for _, assemble := range []func(command.Context) error{p.assets, p.worldRoster, p.gameplay, p.commerce, p.events, p.session} {
		if err := assemble(identity); err != nil {
			return nil, err
		}
	}
	if err := startupTransaction.Commit(); err != nil {
		return nil, err
	}
	startupCommitted = true
	if err := repository.Check(); err != nil {
		return nil, err
	}
	return p, nil
}
