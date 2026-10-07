package app

import (
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/mail"
	"bd2server/internal/server/domain/progression/missions"
	"bd2server/internal/server/domain/roster/deck"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/storage/account"
	"fmt"
	"path/filepath"
)

func (p *playerAssembly) assets(ctx command.Context) error {
	var err error
	p.startingPackID, err = ctx.State.(*accountstate.CommandStore).LockStartingPack(p.options.gameRules.Story.StartPackID, p.initializeAccount)
	if err != nil {
		return fmt.Errorf("server starting chapter policy: %w", err)
	}
	p.progressState, err = progress.OpenStore(ctx, p.scope)
	if err != nil {
		return err
	}
	p.deckStateStore, err = deck.OpenStore(ctx, p.scope, p.deckSeed, *p.design.presetDesign)
	if err != nil {
		return fmt.Errorf("load deck state: %w", err)
	}
	if err := p.login.AttachPresetSlots(p.deckStateStore); err != nil {
		return fmt.Errorf("attach preset slots to login: %w", err)
	}
	if err := p.login.AttachPortrait(p.deckStateStore); err != nil {
		return fmt.Errorf("attach persisted portrait to login: %w", err)
	}
	p.ownedItems, err = assets.OpenInventory(ctx, p.scope, p.starter.Items)
	if err != nil {
		return fmt.Errorf("load owned inventory: %w", err)
	}
	if err := p.ownedItems.AttachItemStacks(p.design.itemStacks); err != nil {
		return err
	}
	p.recipeService, err = assets.NewRecipeService(p.design.recipeDesign, p.starter.CookingRecipes, p.ownedItems)
	if err != nil {
		return fmt.Errorf("load learned recipes: %w", err)
	}
	if err := p.ownedItems.AttachRandomBoxes(ctx, p.design.randomBoxes); err != nil {
		return fmt.Errorf("attach random-box GameData: %w", err)
	}
	gold, freeJewelry, jewelry, mileage, err := p.login.SeedCurrencies()
	if err != nil {
		return fmt.Errorf("read account seed currency: %w", err)
	}
	hopePowder, err := p.login.SeedHopePowder()
	if err != nil {
		return fmt.Errorf("read account seed hope powder: %w", err)
	}
	catalyst, err := p.login.SeedCatalyst()
	if err != nil {
		return fmt.Errorf("read account seed catalyst: %w", err)
	}
	equipMileage, equipMileageExchangeGage, err := p.login.SeedEquipmentMileage()
	if err != nil {
		return fmt.Errorf("read account seed equipment mileage: %w", err)
	}
	p.wallet, err = assets.OpenWallet(ctx, p.scope, assets.Currency{
		Gold: gold, FreeJewelry: freeJewelry, Jewelry: jewelry, Catalyst: catalyst, Mileage: mileage, HopePowder: hopePowder,
		EquipMileage: equipMileage, EquipMileageExchangeGage: equipMileageExchangeGage,
	})
	if err != nil {
		return fmt.Errorf("load wallet state: %w", err)
	}
	if err := p.login.AttachCurrencies(p.wallet); err != nil {
		return fmt.Errorf("attach wallet to login: %w", err)
	}
	itemSlots, storageSlots, equipmentInventorySlots, equipmentStorageSlots, err := p.login.SeedInventorySlots()
	if err != nil {
		return fmt.Errorf("read account seed inventory slots: %w", err)
	}
	p.inventorySlots, err = assets.OpenInventorySlots(ctx, p.scope, p.design.slotDesign, assets.InventorySlotCounts{
		Items: itemSlots, Storage: storageSlots, Equipment: equipmentInventorySlots, EquipmentStorage: equipmentStorageSlots,
	}, p.wallet)
	if err != nil {
		return fmt.Errorf("load inventory slot state: %w", err)
	}
	p.inventorySlots.AttachDevelopmentSettings(p.options.devToolsConfig)
	if err := p.login.AttachInventorySlots(p.inventorySlots); err != nil {
		return fmt.Errorf("attach inventory slots to login: %w", err)
	}
	p.mailService, err = mail.OpenService(ctx, p.scope, p.mailbox, p.ownedItems, p.wallet)
	if err != nil {
		return fmt.Errorf("load mail state: %w", err)
	}
	if err := p.mailService.AttachContentTickets(ctx, p.design.contentTickets); err != nil {
		return fmt.Errorf("attach mailbox content tickets: %w", err)
	}
	if err := p.mailService.AttachSeedPath(ctx, filepath.Clean(p.options.mailSeed)); err != nil {
		return fmt.Errorf("watch mail seed: %w", err)
	}
	if p.options.mailGrantSpool != "" {
		if err := p.mailService.AttachGrantSpoolPath(ctx, p.options.mailGrantSpool); err != nil {
			return fmt.Errorf("attach mail grant spool: %w", err)
		}
	}
	p.missionService, err = missions.Open(ctx, p.scope, p.design.missionDesign, p.ownedItems)
	if err != nil {
		return fmt.Errorf("load mission state: %w", err)
	}
	if err := p.missionService.AttachWallet(ctx, p.wallet); err != nil {
		return fmt.Errorf("attach mission wallet: %w", err)
	}
	if err := p.missionService.AttachUserLevelRewards(ctx, p.design.levelDesign); err != nil {
		return fmt.Errorf("attach user level rewards: %w", err)
	}
	if err := p.login.AttachLevelReward(p.missionService); err != nil {
		return fmt.Errorf("attach persisted user level reward: %w", err)
	}
	if err := p.missionService.AttachMail(ctx, p.mailService); err != nil {
		return fmt.Errorf("attach mission compensation mailbox: %w", err)
	}
	p.ownedEquipment, err = assets.OpenEquipmentInventory(ctx, p.scope)
	if err != nil {
		return fmt.Errorf("load owned equipment: %w", err)
	}
	if err := p.ownedEquipment.AttachSlots(ctx, p.design.equipmentSlots); err != nil {
		return fmt.Errorf("attach equipment slot GameData: %w", err)
	}
	if err := p.ownedEquipment.AttachUpgrade(ctx, p.design.equipmentUpgrade, p.wallet, p.ownedItems); err != nil {
		return fmt.Errorf("attach equipment upgrade GameData: %w", err)
	}
	if err := p.ownedEquipment.AttachCraft(ctx, p.design.equipmentCraft); err != nil {
		return fmt.Errorf("attach equipment crafting GameData: %w", err)
	}
	if err := p.ownedEquipment.AttachSmelting(ctx, p.design.equipmentSmelting, p.wallet, p.ownedItems); err != nil {
		return fmt.Errorf("attach equipment smelting GameData: %w", err)
	}
	if err := p.ownedEquipment.AttachOptionReroll(ctx, p.design.equipmentOptionReroll, p.wallet, p.ownedItems); err != nil {
		return fmt.Errorf("attach equipment option reroll GameData: %w", err)
	}
	return nil
}
