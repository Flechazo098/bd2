package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/commerce"
	"bd2server/internal/server/domain/commerce/gacha"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/domain/profile"
	"bd2server/internal/server/gateway/session"
	"bd2server/internal/server/protocol/defaults"
	scheduleadapter "bd2server/internal/server/protocol/schedule"
	"bd2server/internal/server/protocol/staticdata"
	"bd2server/internal/server/storage/account"
	"context"
	"fmt"
	"time"
)

func (p *playerAssembly) session(ctx command.Context) error {
	if err := p.nickname(ctx); err != nil {
		return err
	}
	if err := p.avatar(ctx); err != nil {
		return err
	}
	if err := p.profile(ctx); err != nil {
		return err
	}
	if err := p.chat(); err != nil {
		return err
	}
	if err := p.multiplayerPVP(ctx); err != nil {
		return err
	}
	p.handlers = []session.Handler{
		p.chatService,
		p.nicknameService,
		p.avatarService,
		friendSupporterDetailHandler{owner: p.ProfileOwner, load: func(ctx command.Context, target uint64) (map[uint64][]byte, error) {
			if p.shared == nil || p.shared.friends == nil {
				return nil, profile.ErrNotFound
			}
			published, err := p.shared.friends.Profile(context.Background(), target)
			return published.SupporterDetails, err
		}},
		p.profileService,
		commerce.PackInfoHandler{World: p.worldService, Claims: p.clearPackages},
		p.worldService,
		p.progressState,
		p.cashService,
		p.cashBonuses,
		p.clearPackages,
		commerce.AttendanceHandler{Events: p.eventTasksService, Economy: p.cashEconomy, LoginPasses: p.loginPasses, Store: p.gameplayStore},
		p.eventRegistry,
		p.eventGamesService,
		p.eventExchangeService,
		p.boxService,
		p.eventPlayService,
		p.fishingService,
		p.eventActionsService,
		p.npcShopService,
		p.innService,
		events.SkinHandler{Economy: p.eventEconomy},
		p.battleService,
		p.huntingService,
		p.monsterHuntService,
		p.worldService.CharacterService(),
		p.progressState,
		p.deckStateStore,
		p.ownedItems,
		p.ownedEquipment,
		p.inventorySlots,
		p.charAwakeService,
		p.costumePotentialService,
		p.costumeBurstService,
		p.friendshipService,
		p.contentOpenService,
		p.masterTitleService,
		p.recruitService,
		p.foodService,
		p.talentUseService,
		p.dispatchService,
		p.itemCraftService,
		p.recipeService,
		p.starter,
		p.mailService,
		p.gachaService,
		p.achievementCounters,
		p.missionService,
		p.eventTasksService,
		p.pictorialService,
		&scheduleadapter.Service{Schedule: p.options.calendars.RegularService},
		readonly.Service{Seed: p.seeds.defaults},
		feature.Service{},
	}
	if p.pvpService != nil {
		p.handlers = append([]session.Handler{p.pvpService}, p.handlers...)
	}
	p.observers = []session.ResponseObserver{p.achievementObserver, p.eventTasksService, p.mailService}
	featured := gacha.ActivePickupCostumes(p.design.regularGacha, p.options.calendars.GachaSeed, uint64(time.Now().UTC().UnixMilli()))
	limitedIDs := p.design.limitedCostumes.Excluding(featured)
	if len(limitedIDs) != 0 {
		if err := p.mailService.EnsureStarterLimitedCostumes(ctx, limitedIDs, time.Now().UTC()); err != nil {
			return fmt.Errorf("ensure account limited-costume entitlement: %w", err)
		}
	}
	prestigeIDs := p.design.prestigeCatalog.Giftable(func(key gamedata.CashProductKey) bool { return p.cashService.IsAvailable(ctx, key) })
	if len(prestigeIDs) != 0 {
		if err := p.mailService.EnsureStarterPrestigeSkins(ctx, prestigeIDs, time.Now().UTC()); err != nil {
			return fmt.Errorf("ensure account prestige-skin entitlement: %w", err)
		}
	}
	if p.initializeAccount {
		if err := ensureAccountStateInitialized(ctx,
			p.progressState, p.deckStateStore, p.ownedItems, p.ownedEquipment,
			p.worldService.CharacterService(), p.collection, p.wallet, p.inventorySlots, p.mailService, p.missionService,
		); err != nil {
			return fmt.Errorf("initialize complete account state generation: %w", err)
		}
		if err := p.worldService.EnsureInitialPackPurchase(ctx); err != nil {
			return fmt.Errorf("grant initial pack purchase rewards: %w", err)
		}
		if err := ctx.State.(*accountstate.CommandStore).MarkInitializationComplete(); err != nil {
			return fmt.Errorf("mark account initialization complete: %w", err)
		}
	}
	if err := p.masterTitleService.EnsurePersisted(ctx); err != nil {
		return fmt.Errorf("persist master title: %w", err)
	}
	problems, err := ctx.State.(*accountstate.CommandStore).Validate()
	if err != nil {
		return fmt.Errorf("validate account state database: %w", err)
	}
	if len(problems) != 0 {
		return stateProblemsError("account state database rejected", problems)
	}
	return nil
}
