package app

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/progression/achievements"
	"context"
)

func (p *playerAssembly) attachAchievementProjection(provider *achievements.OwnedGameplayAchievementProvider) {
	world := provider.Project
	provider.Project = func(ctx command.Context, snapshot *achievements.GameplayAchievementSnapshot) error {
		if err := world(ctx, snapshot); err != nil {
			return err
		}
		add := func(kind, sub, value uint64) {
			snapshot.Conditions = append(snapshot.Conditions, achievements.GameplayAchievementCondition{Type: kind, SubType: sub, Value: value, LowerBound: true})
		}
		recipes := map[uint64]bool{}
		for _, id := range p.starter.CookingRecipes {
			recipes[id] = true
		}
		for key, count := range snapshot.Items {
			if count == 0 {
				continue
			}
			switch key[0] {
			case 7:
				if p.design.recipeDesign.IDs[key[1]] {
					recipes[key[1]] = true
				}
			case 45:
				add(137, key[1], 1)
			case 61:
				add(147, key[1], 1)
			}
		}
		add(6, 0, uint64(len(recipes)))
		currency := p.wallet.Snapshot(ctx)
		add(1, 4, currency.Gold)
		add(1, 22, currency.HopePowder)
		add(79, 0, currency.FreeJewelry+currency.Jewelry)
		add(53, 0, p.missionService.ClaimedMissionCount())
		exp, err := p.missionService.AchievementExperience()
		if err != nil {
			return err
		}
		add(70, 0, p.design.levelDesign.Level(exp))
		var friendship uint64
		for _, entry := range p.collection.FriendshipEntries() {
			if entry.State != nil && entry.State.Level >= p.design.friendshipDesign.Default.MaxLevels[0] {
				friendship++
			}
		}
		add(105, 0, friendship)
		if p.huntingService != nil {
			clears, err := p.huntingService.RecordedClearCounts(ctx)
			if err != nil {
				return err
			}
			for difficulty, count := range clears {
				add(47+difficulty, 0, count)
			}
		}
		if p.fishingService != nil {
			values, err := p.fishingService.AchievementProgress()
			if err != nil {
				return err
			}
			for key, value := range values {
				add(key[0], key[1], value)
			}
		}
		if p.pvpService != nil {
			for key, value := range p.pvpService.RecordedBattleCounts() {
				add(key[0], key[1], value)
			}
		}
		if p.monsterHuntService != nil {
			add(84, 0, p.monsterHuntService.RecordedBattleCount())
		}
		if p.shared != nil && p.shared.friends != nil {
			owner, err := p.ProfileOwner(ctx)
			if err != nil {
				return err
			}
			counts, err := p.shared.friends.GameplayCounts(context.Background(), ctx.AccountID, owner)
			if err != nil {
				return err
			}
			add(101, 0, counts.RentalClaims)
			add(109, 0, counts.BorrowCount)
			add(119, 0, counts.RoomLikes)
		}
		return nil
	}
}
