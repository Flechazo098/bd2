package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle"
	"bd2server/internal/server/domain/battle/hunting"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/commerce/npcinn"
	"bd2server/internal/server/domain/commerce/npcshop"
	"bd2server/internal/server/domain/events"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/world/todayquest"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"fmt"
)

func (p *playerAssembly) gameplay(ctx command.Context) error {
	var err error
	p.battleService = battle.NewService(p.options.gameData, p.options.gameDataVersion, p.worldService.CurrentPackID)
	freeHuntingAP, bonusHuntingAP, err := p.login.SeedHuntingAP()
	if err != nil {
		return fmt.Errorf("read initial hunting AP: %w", err)
	}
	p.gameplayStore = stateio.EntrySnapshotStore{Domain: "missions", Bucket: "gameplay"}
	if err := p.costumePotentialService.AttachConnectStore(p.gameplayStore); err != nil {
		return fmt.Errorf("attach costume potential connection state: %w", err)
	}
	if err := p.worldService.AttachFieldMonsterState(p.gameplayStore); err != nil {
		return fmt.Errorf("attach field monster state: %w", err)
	}
	p.battleService.AttachFieldMonsters(p.worldService)
	p.battleService.AttachFieldBuffConsume(p.worldService.ConsumeFieldBattleBuff)
	if err := p.worldService.AttachFieldBuffRuntime(p.design.fieldBuffs); err != nil {
		return fmt.Errorf("attach field monster damage: %w", err)
	}
	p.contentOpenService, err = assets.NewContentOpenService(ctx, p.design.contentOpeningDesign, p.ownedItems, p.gameplayStore, func() (uint64, error) {
		experience, err := p.missionService.AchievementExperience()
		if err != nil {
			return 0, err
		}
		return p.design.levelDesign.Level(experience), nil
	})
	if err != nil {
		return fmt.Errorf("load content opening state: %w", err)
	}
	p.huntingService, err = hunting.Open(ctx, p.gameplayStore, p.design.source, p.ownedItems, p.wallet,
		p.worldService.CurrentPackID, freeHuntingAP, bonusHuntingAP)
	if err != nil {
		return fmt.Errorf("load hunting state: %w", err)
	}
	if err := p.login.AttachHuntingAP(p.huntingService); err != nil {
		return fmt.Errorf("attach persisted hunting AP: %w", err)
	}
	if err = p.huntingService.AttachAPRefresh(p.design.huntingAPDesign); err != nil {
		return err
	}
	p.battleService.AttachHunting(p.huntingService)
	p.huntingService.AttachEligibility(p.worldService.HuntingEligibility)
	if err := p.worldService.AttachHuntingGround(p.huntingService); err != nil {
		return err
	}
	p.eventRegistry = events.NewRegistry()
	if err := p.eventRegistry.Replace(p.options.calendars.Events); err != nil {
		return err
	}
	initialEventCurrency := map[uint64]uint64{}
	for itemType, field := range events.AdditionalCurrencyFields {
		value, _, readErr := wire.Varint(p.login.UserInfo, field)
		if readErr != nil {
			return readErr
		}
		initialEventCurrency[itemType] = value
	}
	p.eventEconomy, err = events.NewEconomy(ctx, p.gameplayStore, p.ownedItems, p.wallet, p.collection, p.ownedEquipment, p.design.rewardCostumes, p.design.rewardEquipment, p.design.rewardGraph, initialEventCurrency)
	if err != nil {
		return fmt.Errorf("load event economy: %w", err)
	}
	p.eventEconomy.AttachHuntingAP(p.huntingService)
	if err = p.worldService.AttachSkyWay(ctx, p.design.skyway, p.gameplayStore, p.eventEconomy, p.huntingService, p.design.skywaySchedules, p.design.rewardGraph); err != nil {
		return fmt.Errorf("attach SkyWay: %w", err)
	}
	p.battleService.AttachSkyWay(p.worldService)
	p.huntingService.AttachDispatchEligibility(p.worldService.SkyWayDispatchEligibility)
	p.huntingService.AttachSkyWayDispatch(p.worldService.SkyWayDispatchCosts, p.worldService.SkyWayDispatchExchange, p.worldService.SkyWayDispatchBonus)
	p.talentUseService, err = roster.NewTalentUseService(p.design.talentUseDesign, p.gameplayStore, p.worldService.CharacterService(), p.ownedItems, p.wallet, p.eventEconomy)
	if err != nil {
		return fmt.Errorf("load field talent state: %w", err)
	}
	p.talentUseService.AttachContext(ctx, p.worldService.TalentFieldContext)
	if err := p.worldService.AttachAutoRecoveryPolicy(p.design.recovery); err != nil {
		return fmt.Errorf("attach automatic recovery policy: %w", err)
	}
	p.deckStateStore.AttachAutoRecoveryAllowed(p.worldService.AutoRecoveryAllowed)
	p.deckStateStore.AttachAutoRecovery(p.talentUseService.AutoRecover)
	p.worldService.AttachTalentPackInfo(p.talentUseService.PackInfo)
	p.worldService.AttachOverwhelmAuthorization(p.talentUseService.ConsumeOverwhelm)
	p.worldService.AttachOverwhelmHunting(p.huntingService)
	if err := p.worldService.AttachOverwhelmDesign(p.design.source); err != nil {
		return fmt.Errorf("attach overwhelm design: %w", err)
	}
	p.talentUseService.AttachEffect(4, p.worldService.ApplyTalentFieldAbsorb)
	p.talentUseService.AttachEffect(20, p.worldService.ApplyTalentMonsterSummon)
	p.dispatchService, err = roster.OpenTalentDispatch(p.gameplayStore, p.design.dispatchDesign, p.eventEconomy)
	if err != nil {
		return fmt.Errorf("load talent dispatch state: %w", err)
	}
	p.talentUseService.AttachEffect(18, p.dispatchService.Start)
	p.itemCraftService, err = roster.NewItemCraftService(p.design.itemCraftDesign, p.design.talentUseDesign, p.gameplayStore, p.ownedItems, p.worldService.CharacterService(), p.wallet, p.recipeService.Knows)
	if err != nil {
		return fmt.Errorf("load item crafting state: %w", err)
	}
	p.itemCraftService.AttachContext(ctx, func(ctx command.Context) (int, bool, error) {
		pack, err := p.worldService.CurrentPackID(ctx)
		return pack, p.battleService.Active(ctx), err
	})
	if err := p.worldService.ConfigureNPCRuntime(p.design.source, p.gameplayStore); err != nil {
		return fmt.Errorf("configure NPC world runtime: %w", err)
	}
	p.innService, err = npcinn.New(p.gameplayStore, p.worldService.CharacterService(), p.wallet, p.worldService.InnContext,
		func(ctx command.Context) (uint64, error) {
			experience, err := p.missionService.AchievementExperience()
			if err != nil {
				return 0, err
			}
			return p.design.levelDesign.Level(experience), nil
		}, p.battleService.Active)
	if err != nil {
		return fmt.Errorf("load inn recovery: %w", err)
	}
	p.npcShopService, err = npcshop.New(ctx, p.design.npcShopDesign, p.gameplayStore, p.eventEconomy, p.ownedItems, p.worldService.PackAvailable)
	if err != nil {
		return fmt.Errorf("load NPC shop state: %w", err)
	}
	p.npcShopService.SetReputationSource(p.worldService.NPCShopReputation)
	p.npcShopService.SetTalentDiscountSource(p.talentUseService.ShopDiscount)
	p.commissionService, err = todayquest.Open(p.gameplayStore, p.design.commissionDesign, p.eventEconomy, p.ownedItems, p.worldService.CommissionPackUnlocked)
	if err != nil {
		return fmt.Errorf("load NPC commission state: %w", err)
	}
	p.commissionService.CompleteReputation = p.worldService.CompleteNPCReputation
	if err := p.worldService.AttachTodayQuests(p.commissionService); err != nil {
		return fmt.Errorf("attach NPC commissions: %w", err)
	}
	if err = p.worldService.AttachResearchRuntime(p.design.source, p.design.researchCharacters, p.eventEconomy); err != nil {
		return fmt.Errorf("attach field research: %w", err)
	}
	p.eventEconomy.AttachPrestigeSkins(p.design.prestigeCatalog.Skins)
	p.eventEconomy.AttachPrestigePortrait(p.deckStateStore.PortraitCostume)
	if err := p.worldService.AttachPrestigeSelections(ctx, p.eventEconomy.PrestigeSkinSelections); err != nil {
		return fmt.Errorf("attach prestige skin selections: %w", err)
	}
	p.eventEconomy.AttachOwnedItemDesign(p.design.ownedEventItems)
	p.eventEconomy.AttachAvatarRewards(p.design.avatarRewards)
	p.buffRewards, err = events.OpenBuffRewards(ctx, p.gameplayStore, p.design.buffDesign)
	if err != nil {
		return fmt.Errorf("load permanent buff ownership: %w", err)
	}
	p.eventEconomy.AttachBuffRewards(p.buffRewards)
	p.pictorialService.AttachPermanentBuffs(p.buffRewards.SnapshotBuffs)
	if err = p.eventEconomy.AttachAPRefresh(p.design.eventAPCaps, p.design.eventAPReset); err != nil {
		return err
	}
	if err = p.login.AttachAdditionalCurrencies(p.eventEconomy); err != nil {
		return err
	}
	p.huntingService.AttachRewards(func(ctx command.Context, identity string, rewards []gamedata.Reward) ([]byte, error) {
		return p.eventEconomy.Apply(ctx, identity, nil, rewards)
	})
	return nil
}
