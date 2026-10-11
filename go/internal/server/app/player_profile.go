package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/profile"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/social"
	"bd2server/internal/server/protocol/wire"
	"context"
	"errors"
	"fmt"
	"slices"
)

func (p *playerAssembly) profile(ctx command.Context) error {
	initialTitle, _, err := wire.Varint(p.login.UserInfo, 66)
	if err != nil {
		return err
	}
	p.profileService, err = profile.Open(ctx, p.gameplayStore, p, p.design.profileDesign, p.eventEconomy, p.checkProfileText, initialTitle)
	if err != nil {
		return err
	}
	return p.login.AttachProfileTitle(p.profileService)
}

func (p *playerAssembly) PublicProfile(ctx command.Context, target uint64) (profile.Snapshot, error) {
	owner, _, err := wire.Varint(p.login.UserInfo, 1)
	if err != nil {
		return profile.Snapshot{}, err
	}
	if owner != target {
		if p.shared != nil && p.shared.friends != nil {
			published, err := p.shared.friends.Profile(context.Background(), target)
			if err == nil {
				return published.Snapshot, nil
			}
			if !errors.Is(err, social.ErrNotFound) {
				return profile.Snapshot{}, err
			}
		}
		return profile.Snapshot{}, profile.ErrNotFound
	}
	settings, err := p.profileService.Settings(ctx)
	if err != nil {
		return profile.Snapshot{}, err
	}
	exp, err := p.missionService.AchievementExperience()
	if err != nil {
		return profile.Snapshot{}, err
	}
	portrait := p.deckStateStore.PortraitCostume()
	var portraitDesign uint64
	if costume, ok := p.collection.CostumeByID(portrait); ok {
		portraitDesign = costume.DesignID
	}
	rank, percent, err := p.monsterHuntService.ProfileRank()
	if err != nil {
		return profile.Snapshot{}, err
	}
	supporters, err := p.profileService.SupporterBases(ctx, settings)
	if err != nil {
		return profile.Snapshot{}, err
	}
	avatar, err := p.avatarService.CurrentUse(ctx)
	if err != nil {
		return profile.Snapshot{}, err
	}
	return profile.Snapshot{
		OwnerIndex: owner, UserID: p.nicknameService.CurrentNickname(), TitleID: settings.TitleID,
		PortraitCostumeID: portrait, PortraitDesignID: portraitDesign,
		AchievementLevel: p.design.levelDesign.Level(exp), TotalBattlePower: p.deckStateStore.HighestTotalBattlePower(),
		MonsterHuntRank: rank, MonsterHuntTopPercent: percent, Card: settings.Card,
		Greeting: settings.Greeting, IsAllPrivate: settings.IsAllPrivate, Options: settings.Options, SortIDs: settings.SortIDs, Supporters: supporters, AvatarUse: &avatar,
	}, nil
}

func (p *playerAssembly) OwnedItems(ctx command.Context) []assets.Item { return p.ownedItems.All(ctx) }
func (p *playerAssembly) OwnedCostume(id uint64) (profile.CostumeBase, bool) {
	costume, ok := p.collection.CostumeByID(id)
	return profile.CostumeBase{ID: costume.ID, Level: costume.Level, DesignID: costume.DesignID}, ok
}
func (p *playerAssembly) SelectPortrait(ctx command.Context, id uint64) error {
	return p.deckStateStore.SelectPortrait(ctx, id, p.design.profileDesign.DefaultPortraitCostumeID)
}
func (p *playerAssembly) ProfileOwner(command.Context) (uint64, error) {
	owner, _, err := wire.Varint(p.login.UserInfo, 1)
	return owner, err
}
func (p *playerAssembly) CardUnlocked(ctx command.Context, item gamedata.ProfileCardItem) (bool, error) {
	switch item.Unlock {
	case 0:
		return true, nil
	case 1:
		for _, pack := range item.Magic {
			if !p.worldService.CashPackagePackCleared(pack, 0) {
				return false, nil
			}
		}
		return true, nil
	case 2, 3:
		return false, nil
	case 4:
		for _, own := range p.ownedItems.All(ctx) {
			if own.Type == 47 && own.ID == item.ID && own.Count > 0 {
				return true, nil
			}
		}
		return false, nil
	default:
		return false, fmt.Errorf("profile: unsupported card unlock rule %d", item.Unlock)
	}
}
func (p *playerAssembly) SupporterDetails(ctx command.Context, charIndex, costumeIndex uint64) (profile.SupporterDetails, error) {
	character, ok := p.worldService.CharacterService().Find(ctx, charIndex)
	if !ok || roster.IsStoryCharacter(character) || roster.IsCharmCharacter(character) {
		return profile.SupporterDetails{}, fmt.Errorf("profile: supporter character is not permanent-owned")
	}
	costume, ok := p.collection.CostumeByIndex(costumeIndex)
	if !ok {
		return profile.SupporterDetails{}, fmt.Errorf("profile: supporter costume is not owned")
	}
	d, ok := p.design.rewardCostumes.Character(costume.ID)
	if !ok || character.ID != d.ID && !slices.Contains(d.GrowthCharacterIDs, character.ID) {
		return profile.SupporterDetails{}, fmt.Errorf("profile: supporter costume belongs to a different character")
	}
	unique, ok := p.design.charAwakeDesign.CharacterUniqueID(character.ID)
	if !ok {
		return profile.SupporterDetails{}, fmt.Errorf("profile: supporter character has no static identity")
	}
	details := profile.SupporterDetails{Character: character, UniqueID: unique}
	details.Awake, _ = p.collection.CharAwakeState(unique)
	for _, owned := range p.collection.Costumes() {
		design, ok := p.design.rewardCostumes.Character(owned.ID)
		if ok {
			uid, known := p.design.charAwakeDesign.CharacterUniqueID(design.ID)
			if known && uid == unique {
				details.Costumes = append(details.Costumes, owned)
			}
		}
	}
	for _, equipment := range p.ownedEquipment.All(ctx) {
		if equipment.UseChar == charIndex {
			details.Equipment = append(details.Equipment, equipment)
		}
	}
	return details, nil
}
