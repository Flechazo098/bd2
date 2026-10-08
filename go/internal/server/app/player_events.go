package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle/monsterhunt"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/domain/events/actions"
	"bd2server/internal/server/domain/events/exchange"
	"bd2server/internal/server/domain/events/games"
	"bd2server/internal/server/domain/events/play"
	"bd2server/internal/server/domain/events/tasks"
	"bd2server/internal/server/domain/progression/achievements"
	"bd2server/internal/server/domain/progression/missions"
	"bd2server/internal/server/domain/roster"
	"fmt"
	"log/slog"
	"path/filepath"
)

func (p *playerAssembly) events(ctx command.Context) error {
	var err error
	p.eventGamesService, err = eventgames.Open(ctx, p.gameplayStore, p.design.source.EventGame, p.eventRegistry, p.eventEconomy)
	if err != nil {
		return fmt.Errorf("load event games state: %w", err)
	}
	p.eventExchangeService, err = eventexchange.Open(ctx, p.gameplayStore, p.design.eventExchangeDesign, p.eventRegistry, p.eventEconomy)
	if err != nil {
		return fmt.Errorf("load event exchange state: %w", err)
	}
	p.boxService, err = events.OpenBoxes(p.gameplayStore, p.ownedItems, p.eventEconomy)
	if err != nil {
		return fmt.Errorf("load random box state: %w", err)
	}
	p.eventPlayService, err = eventplay.Open(ctx, p.gameplayStore, p.design.eventPlay, func(id uint64) (*gamedata.EventField, error) {
		return p.design.source.EventField(p.design.eventPlay, id)
	}, p.eventRegistry, p.eventEconomy)
	if err != nil {
		return fmt.Errorf("load event play state: %w", err)
	}
	p.eventPlayService.AttachBattleChallenges(p.design.eventBattleChallenges)
	p.eventPlayService.AttachHubCalendars(p.seeds.defaults)
	if err := p.eventPlayService.AttachFieldBindingsFile(filepath.Join(filepath.Dir(p.options.worldSeed), "event_field_bindings.json")); err != nil {
		return fmt.Errorf("attach hidden field bindings: %w", err)
	}
	if err := p.worldService.AttachEventFieldPacks(p.eventPlayService); err != nil {
		return fmt.Errorf("attach event field packs: %w", err)
	}
	p.battleService.AttachEventBattle(p.eventPlayService)
	p.eventActionsService, err = eventactions.Open(ctx, p.gameplayStore, p.design.eventActionsDesign, p.eventRegistry, p.eventEconomy)
	if err != nil {
		return fmt.Errorf("load event action state: %w", err)
	}
	if err = p.eventActionsService.AttachMiniContent(ctx, p.eventPlayService, p.design.miniContent); err != nil {
		return fmt.Errorf("attach mini event content: %w", err)
	}
	p.eventActionsService.AttachFriendshipLevel(func(id uint64) uint64 {
		for _, entry := range p.collection.FriendshipEntries() {
			if entry.State != nil && entry.State.CostumeID == id {
				return entry.State.Level
			}
		}
		return 0
	})
	p.eventActionsService.AttachChargeInfo(func(ctx command.Context) ([]byte, error) {
		rows, err := p.eventEconomy.ChargeInfo(ctx)
		if err != nil {
			return nil, err
		}
		huntingRows, err := p.huntingService.APChargeInfo(ctx)
		if err != nil {
			return nil, err
		}
		return append(rows, huntingRows...), nil
	})
	p.eventActionsService.AttachProgress(func(ctx command.Context, condition, sub, count uint64) error {
		if err := p.missionService.RecordEvent(ctx, condition, sub, count, p.worldService.MissionsUnlocked); err != nil {
			return err
		}
		return p.eventTasksService.RecordEvent(ctx, condition, sub, count, p.worldService.MissionsUnlocked)
	})
	p.eventPlayService.AttachProgress(func(ctx command.Context, condition, sub, count uint64) error {
		return p.missionService.RecordEvent(ctx, condition, sub, count, p.worldService.MissionsUnlocked)
	})
	p.eventTasksService.AttachAssociatedMissionGroup(func(schedule events.Schedule) uint64 {
		if group := p.eventActionsService.AssociatedMissionGroup(schedule); group != 0 {
			return group
		}
		group, err := p.eventPlayService.AssociatedMissionGroup(schedule)
		if err != nil {
			slog.Error("event mission design unavailable", "event_uid", schedule.UID, "event_id", schedule.ID, "error", err)
		}
		return group
	})
	p.battleService.AttachEventBattle(p.eventActionsService)
	p.battleService.AttachCurrentDifficulty(p.worldService.CurrentQuestDifficulty)
	p.battleService.AttachQuestBattleValidation(p.worldService.ValidateQuestBattle)
	p.battleService.AttachRewards(func(ctx command.Context, identity string, rewards []gamedata.Reward) ([]byte, error) {
		return p.eventEconomy.Apply(ctx, identity, nil, rewards)
	})
	if err := p.worldService.AttachBattleActive(p.battleService.Active); err != nil {
		return fmt.Errorf("attach world battle guard: %w", err)
	}

	p.monsterHuntService, err = monsterhunt.Open(ctx, p.gameplayStore, p.design.monsterHunt, p.ownedItems, p.wallet)
	if err != nil {
		return fmt.Errorf("load monster hunt state: %w", err)
	}
	if err := p.monsterHuntService.AttachPresetRuntime(ctx, p.worldService.CharacterService(), p.ownedEquipment, p.collection); err != nil {
		return fmt.Errorf("attach monster hunt preset ownership: %w", err)
	}
	if err := p.login.AttachMonsterHuntSlots(p.monsterHuntService); err != nil {
		return fmt.Errorf("attach monster hunt preset slots: %w", err)
	}
	p.battleService.AttachMonsterHunt(p.monsterHuntService)
	p.monsterHuntService.AttachRewards(func(ctx command.Context, identity string, rewards []gamedata.Reward) ([]byte, error) {
		return p.eventEconomy.Apply(ctx, identity, nil, rewards)
	})
	p.recruitService, err = roster.NewRecruitService(&p.design.recruitDesign, &p.design.recruitDesign, p.collection, p.ownedItems, p.wallet,
		func(ctx command.Context, npcID uint64) (uint64, error) {
			return p.worldService.ResolveRecruitNPC(ctx, npcID, p.design.source, &p.design.recruitDesign)
		})
	if err != nil {
		return fmt.Errorf("load recruitment service: %w", err)
	}
	p.battleService.AttachCommittedHealth(func(ctx command.Context, health map[uint64]uint64) error {
		for index, hp := range health {
			maximum, err := p.worldService.CharacterService().MaxHealth(ctx, index)
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
			if err := p.worldService.CharacterService().SetCurrentHealth(ctx, index, hp); err != nil {
				return err
			}
		}
		return nil
	})
	p.foodService, err = roster.OpenFoodService(ctx, p.scope, p.design.foodDesign, p.ownedItems, p.worldService.CharacterService())
	if err != nil {
		return fmt.Errorf("load food state: %w", err)
	}
	if err := p.foodService.AttachContext(ctx, p.worldService.CurrentPackID, p.battleService.Active); err != nil {
		return err
	}
	p.battleService.AttachMonsterWinMission(func(ctx command.Context) error {
		return p.missionService.CompleteSingleTargetEvent(ctx, missions.ConditionMonsterKill, p.worldService.MissionsUnlocked)
	})
	p.battleService.AttachPictorialBuffs(func(ctx command.Context) ([]gamedata.PictorialBuffStat, error) {
		_, buffs, err := p.pictorialService.Snapshot(ctx)
		return buffs, err
	})
	p.achievementCounters, err = achievements.NewAchievementService(p.design.achievementCounterDesign, p.scope, p.missionService)
	if err != nil {
		return fmt.Errorf("load achievement counter state: %w", err)
	}
	if err := p.missionService.AttachAchievementProgress(ctx, p.achievementCounters); err != nil {
		return fmt.Errorf("attach achievement completion validation: %w", err)
	}
	p.commissionService.CompleteAchievement = func(ctx command.Context, identity string) error {
		_, err := p.achievementCounters.RecordEvent(ctx, identity, 17, 0, 1)
		return err
	}
	if err := p.login.AttachAchievementExperience(p.missionService); err != nil {
		return fmt.Errorf("attach persisted achievement experience: %w", err)
	}
	achievementProvider := p.worldService.GameplayAchievementProvider(ctx, p.design.achievementCounterDesign, p.design.achievementGrades)
	achievementProvider.StateVersion = p.stateRepository.ObservationVersion
	p.achievementObserver, err = achievements.NewGameplayAchievementObserver(p.achievementCounters, achievementProvider)
	if err != nil {
		return fmt.Errorf("initialize achievement gameplay observer: %w", err)
	}
	if err := p.achievementObserver.SyncRecordedHistory(ctx); err != nil {
		return fmt.Errorf("restore recorded achievement history: %w", err)
	}
	p.eventTasksService.AttachInventoryProvider(&eventtasks.InventoryProjection{Items: p.ownedItems, Equipment: p.ownedEquipment, Costumes: p.collection, StateVersion: p.stateRepository.ObservationVersion})
	p.worldService.AttachSkyWayProgress(p.eventTasksService.RecordSkyWayClear, func(ctx command.Context, group uint64) error {
		return p.missionService.RecordEvent(ctx, 227, group, 1, p.worldService.MissionsUnlocked)
	})
	p.huntingService.AttachDispatchProgress(func(ctx command.Context, count uint64) error {
		return p.missionService.RecordEvent(ctx, 23, 0, count, p.worldService.MissionsUnlocked)
	})
	return nil
}
