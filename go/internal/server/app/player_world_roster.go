package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/commerce/gacha"
	"bd2server/internal/server/domain/progression/missions"
	"bd2server/internal/server/domain/progression/pictorial"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/world"
	"bd2server/internal/server/protocol/wire"
	"errors"
	"fmt"
)

func (p *playerAssembly) worldRoster(ctx command.Context) error {
	var err error
	p.worldService, err = world.New(ctx, p.worldSeed, p.design.world, p.design.source,
		p.scope, p.progressState, p.starter, p.ownedEquipment, p.ownedItems, p.wallet)
	if err != nil {
		return fmt.Errorf("load world state: %w", err)
	}
	if err := p.worldService.ConfigureStartPack(ctx, p.startingPackID, p.initializeAccount); err != nil {
		return fmt.Errorf("configure account starting chapter: %w", err)
	}

	if err := p.login.AttachLastPlayedPack(p.worldService); err != nil {
		return fmt.Errorf("attach persisted login destination: %w", err)
	}
	// Restore all earned seed ownership before validating persisted upgrades.
	// A quest costume is not a p.collection entry; attaching it after opening
	// p.collection would reject its otherwise valid burst ledger on restart.
	baseCostumes := append([]roster.Costume(nil), p.starter.Costumes...)
	if reward, earned := p.worldService.EarnedQuestCostume(); earned {
		baseCostumes = append(baseCostumes, reward)
	}
	p.collection, err = roster.OpenCollectionStore(ctx, p.scope, baseCostumes)
	if err != nil {
		return fmt.Errorf("load owned collection: %w", err)
	}
	p.gachaService, err = gacha.NewService(p.design.infiniteGacha, p.design.regularGacha, p.collection, p.wallet)
	if err != nil {
		return err
	}
	if err := p.login.AttachPurchaseCounts(p.gachaService); err != nil {
		return fmt.Errorf("attach cash purchase counts to login: %w", err)
	}
	if err := p.gachaService.AttachSchedule(ctx, p.options.calendars.GachaSeed); err != nil {
		return fmt.Errorf("attach gacha schedule: %w", err)
	}
	previewEventIndex, err := p.seeds.defaults.CashProductEventIndex(p.design.infiniteGacha.ProductGroupID, p.design.infiniteGacha.ProductID)
	if err != nil {
		return fmt.Errorf("load infinite preview event: %w", err)
	}
	if err := p.gachaService.AttachPreviewEventIndex(ctx, previewEventIndex); err != nil {
		return fmt.Errorf("attach infinite preview event: %w", err)
	}
	// The mapped client property is IsDoneFirstGachaPick. Its authoritative
	// local state is the explicit GachaSubType=3 completion marker.
	if err := p.login.AttachFirstGacha(p.gachaService); err != nil {
		return fmt.Errorf("attach first gacha status to login: %w", err)
	}
	if err := p.gachaService.AttachFirstGacha(ctx, p.design.firstGacha); err != nil {
		return fmt.Errorf("attach first gacha GameData: %w", err)
	}
	p.gachaService.AttachInventory(p.ownedItems)
	p.gachaService.AttachEquipmentGacha(p.design.equipmentGacha, p.ownedEquipment)
	p.gachaService.AttachDrawMission(func(ctx command.Context, count uint64) error {
		return p.missionService.RecordEvent(ctx, missions.ConditionGachaBuy, 0, count, p.worldService.MissionsUnlocked)
	})
	var permanentBaseCharacters []roster.Character
	for _, c := range p.worldService.CharacterService().RawAll() {
		if !roster.IsCharmCharacter(c) {
			permanentBaseCharacters = append(permanentBaseCharacters, c)
		}
	}
	if err := p.collection.BindBaseCharacters(ctx, permanentBaseCharacters); err != nil {
		return fmt.Errorf("bind base collection characters: %w", err)
	}
	if err := p.mailService.AttachCostumeRewards(ctx, p.collection, p.design.limitedCostumes); err != nil {
		return fmt.Errorf("attach limited costume mail rewards: %w", err)
	}
	if err := p.worldService.AttachCollection(ctx, p.collection); err != nil {
		return fmt.Errorf("attach gacha collection state: %w", err)
	}
	if err := p.worldService.AttachDecks(ctx, p.deckStateStore); err != nil {
		return fmt.Errorf("attach world deck state: %w", err)
	}
	if err := p.worldService.AttachWaypointRuntime(ctx, p.design.source); err != nil {
		return fmt.Errorf("attach waypoint runtime: %w", err)
	}
	if err := p.worldService.AttachFieldObjectRuntime(p.design.source, p.design.fieldReset); err != nil {
		return fmt.Errorf("attach field object runtime: %w", err)
	}
	if err := p.ownedEquipment.AttachCharacters(ctx, p.worldService.CharacterService()); err != nil {
		return fmt.Errorf("attach equipment character state: %w", err)
	}
	if err := p.deckStateStore.AttachPresetRuntime(ctx, p.wallet, p.worldService.CharacterService(), p.ownedEquipment, p.collection); err != nil {
		return fmt.Errorf("attach ordinary preset runtime: %w", err)
	}
	if err := p.deckStateStore.AttachFieldSettingsPack(p.worldService.CurrentPackID); err != nil {
		return err
	}
	if err := p.deckStateStore.AttachFieldSettings(ctx, p.design.fieldSettingsDesign); err != nil {
		return fmt.Errorf("attach field character settings: %w", err)
	}
	if err := p.login.AttachAutoReviveSettings(p.deckStateStore); err != nil {
		return fmt.Errorf("attach automatic revival settings: %w", err)
	}
	p.pictorialService = &pictorial.Service{Design: p.design.pictorialDesign, Owned: p.worldService}
	if err := p.ownedEquipment.AttachStatDesign(ctx, p.design.equipmentStatDesign); err != nil {
		return err
	}
	p.pictorialService.EquipmentContributions = func(ctx command.Context, character roster.Character) ([]gamedata.StatContribution, error) {
		return p.ownedEquipment.StatContributions(ctx, character.InvenIndex)
	}
	p.charAwakeService, err = roster.NewCharAwakeService(p.design.charAwakeDesign, p.collection, p.worldService.CharacterService(), p.ownedItems, p.wallet)
	if err != nil {
		return err
	}
	p.pictorialService.AwakeContributions = p.charAwakeService.Contributions
	if err := p.worldService.CharacterService().AttachMaxHealth(ctx, p.pictorialService.MaxHealth); err != nil {
		return fmt.Errorf("attach pictorial character stats: %w", err)
	}
	if err := p.worldService.CharacterService().AttachWallet(ctx, p.wallet); err != nil {
		return fmt.Errorf("attach character promotion wallet: %w", err)
	}
	if err := p.worldService.CharacterService().AttachTalentGrowth(ctx, p.design.talentGrowth); err != nil {
		return fmt.Errorf("attach character talent growth: %w", err)
	}
	if err := p.worldService.CharacterService().AttachImmortalDesign(ctx, p.design.immortalDesign); err != nil {
		return fmt.Errorf("attach immortal talent GameData: %w", err)
	}
	p.costumePotentialService, err = roster.NewCostumePotentialService(p.design.costumePotentialDesign, p.collection, p.worldService.CharacterService(), p.ownedItems, p.wallet)
	if err != nil {
		return err
	}
	p.pictorialService.PotentialContributions = p.costumePotentialService.Contributions
	p.costumeBurstService, err = roster.NewCostumeBurstService(p.design.costumeBurstDesign, p.collection, p.ownedItems, p.wallet)
	if err != nil {
		return err
	}
	p.friendshipService, err = roster.NewFriendshipService(&p.design.friendshipDesign, p.design.charAwakeDesign, p.design.costumePotentialDesign, p.collection, p.ownedItems, p.wallet)
	if err != nil {
		return fmt.Errorf("load friendship state: %w", err)
	}
	if err := p.login.AttachFriendshipAP(p.friendshipService); err != nil {
		return err
	}
	accountName, found, err := wire.Bytes(p.login.UserInfo, 2)
	if err != nil || !found || len(accountName) == 0 {
		return errors.New("account seed requires its existing display name for master title")
	}
	p.masterTitleService, err = roster.OpenMasterTitleService(ctx, p.scope, string(accountName))
	if err != nil {
		return fmt.Errorf("load master title: %w", err)
	}
	return nil
}
