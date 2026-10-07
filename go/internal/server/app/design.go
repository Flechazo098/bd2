package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle/monsterhunt"
	"bd2server/internal/server/domain/commerce"
	"fmt"
)

type designCatalog struct {
	cashCatalog              *commerce.Catalog
	source                   *gamedata.Source
	world                    *gamedata.WorldRules
	fieldReset               gamedata.FieldResetSchedule
	fieldBuffs               map[uint64]gamedata.FieldBuffDesign
	recovery                 *gamedata.PackRecoveryPolicy
	researchCharacters       map[uint64]bool
	overwhelmSky             []gamedata.SkyWayOverwhelmRule
	eventPlay                *gamedata.EventPlayCatalog
	monsterHunt              *monsterhunt.Rules
	presetDesign             *gamedata.PresetDesign
	recipeDesign             *gamedata.CookingRecipeDesign
	randomBoxes              *gamedata.RandomBoxDesign
	itemStacks               *gamedata.ItemStackDesign
	slotDesign               *gamedata.InventorySlotDesign
	contentTickets           *gamedata.GachaContentTicketDesign
	missionDesign            *gamedata.MissionDesign
	levelDesign              *gamedata.AchievementLevelDesign
	equipmentSlots           map[uint64]uint64
	equipmentUpgrade         *gamedata.EquipmentUpgradeDesign
	equipmentCraft           *gamedata.EquipmentCraftDesign
	talentGrowth             *gamedata.TalentGrowthDesign
	equipmentSmelting        *gamedata.EquipmentSmeltingDesign
	equipmentOptionReroll    *gamedata.EquipmentOptionRerollDesign
	infiniteGacha            *gamedata.InfiniteGachaDesign
	fieldSettingsDesign      *gamedata.FieldSettingsDesign
	pictorialDesign          *gamedata.PictorialDesign
	equipmentStatDesign      *gamedata.EquipmentStatDesign
	charAwakeDesign          *gamedata.CharAwakeDesign
	immortalDesign           *gamedata.ImmortalDesign
	costumePotentialDesign   *gamedata.CostumePotentialDesign
	costumeBurstDesign       *gamedata.CostumeBurstDesign
	friendshipDesign         gamedata.FriendshipDesign
	contentOpeningDesign     *gamedata.ContentOpeningDesign
	huntingAPDesign          gamedata.HuntingAPDesign
	rewardGraph              *gamedata.RewardGraph
	rewardEquipment          *gamedata.EquipmentGachaCatalog
	rewardCostumes           *gamedata.RegularGachaCatalog
	talentUseDesign          *gamedata.TalentUseDesign
	dispatchDesign           map[uint64]gamedata.TalentDispatchDesign
	itemCraftDesign          *gamedata.ItemCraftDesign
	npcShopDesign            gamedata.NPCShopDesign
	commissionDesign         *gamedata.TodayQuestCatalog
	prestigeCatalog          *gamedata.PrestigeSkinCatalog
	ownedEventItems          map[uint64]map[uint64]bool
	avatarRewards            *gamedata.AvatarRewardDesign
	buffDesign               map[uint64]gamedata.PictorialBuffStat
	eventAPCaps              map[uint64]uint64
	eventAPReset             gamedata.HuntingAPDesign
	cashDesign               *gamedata.CashCatalog
	cashEntitlementDesign    *gamedata.CashEntitlementDesign
	cashRewards              *gamedata.CashRewardResolver
	cashMailTemplates        map[uint64]bool
	clearPackageDesign       *gamedata.ClearPackageCatalog
	cashBonusDesign          *gamedata.CashBonusCatalog
	eventTasksDesign         *gamedata.EventTasksDesign
	loginPassDesign          *gamedata.LoginPassCatalog
	eventExchangeDesign      *gamedata.EventExchangeCatalog
	eventBattleChallenges    gamedata.EventBattleChallenges
	eventActionsDesign       *gamedata.EventActionsDesign
	miniContent              *gamedata.MiniContentDesign
	recruitDesign            gamedata.RecruitDesign
	foodDesign               *gamedata.FoodDesign
	achievementCounterDesign *gamedata.AchievementCounterDesign
	achievementGrades        gamedata.GameplayAchievementGrades
	regularGacha             *gamedata.RegularGachaCatalog
	equipmentGacha           *gamedata.EquipmentGachaCatalog
	limitedCostumes          *gamedata.LimitedCostumeCatalog
	firstGacha               *gamedata.FirstGachaDesign
}

func loadDesign(c *configuration, seeds *seedCatalog) (*designCatalog, error) {
	gameData, gameDataVersion := c.gameData, c.gameDataVersion
	calendars, gameRules := c.calendars, c.gameRules
	d := &designCatalog{source: gamedata.NewSource(gameData, gameDataVersion)}
	var err error
	gachaSchedule := calendars.GachaSeed
	var scheduleGroupIDs, stepUpGroupIDs []uint64
	for _, window := range gachaSchedule.Schedules {
		scheduleGroupIDs = append(scheduleGroupIDs, window.GroupID)
	}
	for _, window := range gachaSchedule.StepUps {
		stepUpGroupIDs = append(stepUpGroupIDs, window.GroupID)
	}
	d.regularGacha, d.equipmentGacha, err = gamedata.LoadActiveGachaForSchedules(gameData, gameDataVersion, scheduleGroupIDs, stepUpGroupIDs)
	if err != nil {
		return nil, fmt.Errorf("load active gacha GameData: %w", err)
	}
	if gameRules.Gacha.IncludeCollaborationURWeapons {
		if err := d.equipmentGacha.IncludeCollaborationURWeapons(gameData, gameDataVersion); err != nil {
			return nil, fmt.Errorf("apply collaboration UR weapon game rule: %w", err)
		}
	}
	d.presetDesign, err = gamedata.LoadPresetDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load party preset GameData: %w", err)
	}
	d.recipeDesign, err = gamedata.LoadCookingRecipeDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load cooking recipes: %w", err)
	}
	d.randomBoxes, err = gamedata.LoadRandomBoxDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load deterministic random-box GameData: %w", err)
	}
	d.itemStacks, err = gamedata.LoadItemStackDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load item stack GameData: %w", err)
	}
	d.slotDesign, err = gamedata.LoadInventorySlotDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load inventory slot GameData: %w", err)
	}
	d.contentTickets, err = gamedata.LoadGachaContentTicketDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load gacha content tickets: %w", err)
	}
	d.missionDesign, err = gamedata.LoadMissionDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load mission GameData: %w", err)
	}
	d.levelDesign, err = gamedata.LoadAchievementLevelDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load user level rewards: %w", err)
	}
	d.equipmentSlots, err = gamedata.LoadEquipmentSlots(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load equipment slot GameData: %w", err)
	}
	d.equipmentUpgrade, err = gamedata.LoadEquipmentUpgradeDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load equipment upgrade GameData: %w", err)
	}
	d.equipmentCraft, err = gamedata.LoadEquipmentCraftDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load equipment crafting GameData: %w", err)
	}
	d.talentGrowth, err = gamedata.LoadTalentGrowthDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load talent growth GameData: %w", err)
	}
	d.equipmentSmelting, err = gamedata.LoadEquipmentSmeltingDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load equipment smelting GameData: %w", err)
	}
	d.equipmentOptionReroll, err = gamedata.LoadEquipmentOptionRerollDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load equipment option reroll GameData: %w", err)
	}
	d.infiniteGacha, err = gamedata.LoadInfiniteGachaForSchedules(gameData, gameDataVersion, scheduleGroupIDs)
	if err != nil {
		return nil, fmt.Errorf("load infinite gacha GameData: %w", err)
	}
	d.fieldSettingsDesign, err = gamedata.LoadFieldSettingsDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load field character settings: %w", err)
	}
	d.pictorialDesign, err = gamedata.LoadPictorialDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load pictorial GameData: %w", err)
	}
	d.equipmentStatDesign, err = gamedata.LoadEquipmentStatDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load equipment stat GameData: %w", err)
	}
	d.charAwakeDesign, err = gamedata.LoadCharAwakeDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load character awakening GameData: %w", err)
	}
	d.immortalDesign, err = gamedata.LoadImmortalDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load immortal talent GameData: %w", err)
	}
	d.costumePotentialDesign, err = gamedata.LoadCostumePotentialDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load costume potential GameData: %w", err)
	}
	d.costumeBurstDesign, err = gamedata.LoadCostumeBurstDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load costume burst GameData: %w", err)
	}
	d.friendshipDesign, err = gamedata.LoadFriendshipDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load friendship GameData: %w", err)
	}
	d.contentOpeningDesign, err = gamedata.LoadContentOpeningDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load content opening GameData: %w", err)
	}
	d.huntingAPDesign, err = gamedata.LoadHuntingAPDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load hunting AP reset: %w", err)
	}
	d.rewardGraph, err = gamedata.LoadRewardGraph(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load event reward graph: %w", err)
	}
	d.rewardEquipment, err = gamedata.LoadRewardEquipmentCatalog(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load reward equipment: %w", err)
	}
	d.rewardCostumes, err = gamedata.LoadRewardCostumeCatalog(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load reward costumes: %w", err)
	}
	d.talentUseDesign, err = gamedata.LoadTalentUseDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load field talent skills: %w", err)
	}
	d.dispatchDesign, err = gamedata.LoadTalentDispatchDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load talent dispatch design: %w", err)
	}
	d.itemCraftDesign, err = gamedata.LoadItemCraftDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load item crafting design: %w", err)
	}
	d.npcShopDesign, err = gamedata.LoadNPCShopDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load NPC shop design: %w", err)
	}
	d.commissionDesign, err = gamedata.LoadTodayQuests(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load NPC commission design: %w", err)
	}
	d.prestigeCatalog, err = gamedata.LoadPrestigeSkinCatalog(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load reward prestige skins: %w", err)
	}
	d.ownedEventItems, err = gamedata.LoadOwnedEventItemDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load event inventory design: %w", err)
	}
	d.avatarRewards, err = gamedata.LoadAvatarRewardDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load avatar rewards: %w", err)
	}
	d.buffDesign, err = gamedata.LoadBuffRewardDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load permanent buff rewards: %w", err)
	}
	d.eventAPCaps, d.eventAPReset, err = gamedata.LoadEventAPDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load event AP reset: %w", err)
	}
	d.cashDesign, err = gamedata.LoadCashCatalog(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load cash products: %w", err)
	}
	d.cashEntitlementDesign, err = gamedata.LoadCashEntitlementDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load cash entitlement design: %w", err)
	}
	d.cashRewards, err = gamedata.LoadCashRewardResolver(gameData, gameDataVersion, d.rewardGraph)
	if err != nil {
		return nil, fmt.Errorf("load cash product rewards: %w", err)
	}
	d.cashMailTemplates, err = gamedata.LoadCashMailTemplates(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load cash mail templates: %w", err)
	}
	d.clearPackageDesign, err = gamedata.LoadClearPackageCatalog(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load clear-package rewards: %w", err)
	}
	d.cashBonusDesign, err = gamedata.LoadCashBonusCatalog(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load cash bonus design: %w", err)
	}
	d.eventTasksDesign, err = gamedata.LoadEventTasksDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load event tasks design: %w", err)
	}
	d.loginPassDesign, err = gamedata.LoadLoginPassCatalog(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load login-pass rewards: %w", err)
	}
	d.eventExchangeDesign, err = gamedata.LoadEventExchangeCatalog(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load event exchange design: %w", err)
	}
	d.eventBattleChallenges, err = gamedata.LoadEventBattleChallenges(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load event battle challenges: %w", err)
	}
	d.eventActionsDesign, err = gamedata.LoadEventActionsDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load event action design: %w", err)
	}
	d.miniContent, err = gamedata.LoadMiniContentDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load mini event content: %w", err)
	}
	d.recruitDesign, err = gamedata.LoadRecruitDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load recruitment GameData: %w", err)
	}
	d.foodDesign, err = gamedata.LoadFoodDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load food GameData: %w", err)
	}
	d.achievementCounterDesign, err = gamedata.LoadAchievementCounterDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load achievement counters: %w", err)
	}
	d.achievementGrades, err = gamedata.LoadGameplayAchievementGrades(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load achievement gameplay grades: %w", err)
	}
	d.limitedCostumes, err = gamedata.LoadLimitedCostumes(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load limited costume GameData: %w", err)
	}
	d.firstGacha, err = gamedata.LoadFirstGacha(gameData, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("load first gacha GameData: %w", err)
	}
	d.world, err = gamedata.LoadWorldRules(gameData, gameDataVersion, seeds.world.PlaceholderCostumeID)
	if err != nil {
		return nil, err
	}
	d.fieldReset, err = gamedata.LoadFieldResetSchedule(gameData, gameDataVersion)
	if err != nil {
		return nil, err
	}
	d.fieldBuffs, err = gamedata.LoadFieldBuffDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, err
	}
	d.recovery, err = gamedata.LoadPackRecoveryPolicy(gameData, gameDataVersion)
	if err != nil {
		return nil, err
	}
	d.researchCharacters, err = gamedata.LoadResearchCharacters(gameData, gameDataVersion)
	if err != nil {
		return nil, err
	}
	d.overwhelmSky, err = gamedata.LoadSkyWayOverwhelm(gameData, gameDataVersion)
	if err != nil {
		return nil, err
	}
	d.eventPlay, err = gamedata.LoadEventPlayCatalog(gameData, gameDataVersion)
	if err != nil {
		return nil, err
	}
	presets, err := gamedata.LoadMonsterHuntPresetDesign(gameData, gameDataVersion)
	if err != nil {
		return nil, err
	}
	seasons := monsterHuntSeasons(seeds.defaults)
	d.monsterHunt, err = monsterhunt.NewRules(seasons, seeds.defaults, presets, d.source.MonsterHunt)
	if err != nil {
		return nil, err
	}
	d.cashCatalog, err = commerce.NewCatalog(c.versions.GameVersion, d.cashDesign, c.gameRules.Purchases)
	if err != nil {
		return nil, err
	}
	return d, nil
}
