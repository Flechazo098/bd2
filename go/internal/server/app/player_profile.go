package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle/pvp"
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
	"time"
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
				published.Snapshot.IsFriend, err = p.shared.friends.IsFriend(context.Background(), owner, target)
				if err != nil {
					return profile.Snapshot{}, err
				}
				published.Snapshot.RoomInfo, published.Snapshot.RoomLikeCount, err = p.shared.friends.RoomProfile(context.Background(), owner, target)
				if err == nil {
					err = p.profileRankings(target, &published.Snapshot)
				}
				return published.Snapshot, err
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
	var guildBase []byte
	if p.shared != nil && p.shared.guild != nil {
		_, guildBase, err = p.shared.guild.LoginInfo(context.Background(), ctx.AccountID)
		if err != nil {
			return profile.Snapshot{}, err
		}
	}
	var roomInfo []byte
	var roomLikes uint64
	var likes uint64
	if p.shared != nil && p.shared.friends != nil {
		roomInfo, roomLikes, err = p.shared.friends.RoomProfile(context.Background(), owner, owner)
		if err != nil {
			return profile.Snapshot{}, err
		}
		likes, err = p.shared.friends.LikeCount(context.Background(), owner)
		if err != nil {
			return profile.Snapshot{}, err
		}
	}
	snapshot := profile.Snapshot{
		RoomInfo: roomInfo, RoomLikeCount: roomLikes, GuildBase: guildBase, LikeCount: likes, OwnerIndex: owner, UserID: p.nicknameService.CurrentNickname(), TitleID: settings.TitleID,
		PortraitCostumeID: portrait, PortraitDesignID: portraitDesign,
		AchievementLevel: p.design.levelDesign.Level(exp), TotalBattlePower: p.deckStateStore.HighestTotalBattlePower(),
		MonsterHuntRank: rank, MonsterHuntTopPercent: percent, Card: settings.Card,
		Greeting: settings.Greeting, IsAllPrivate: settings.IsAllPrivate, Options: settings.Options, SortIDs: settings.SortIDs, Supporters: supporters, AvatarUse: &avatar,
	}
	if err := p.profileRankings(owner, &snapshot); err != nil {
		return profile.Snapshot{}, err
	}
	return snapshot, nil
}

func (p *playerAssembly) profileRankings(owner uint64, snapshot *profile.Snapshot) error {
	if p.shared == nil {
		return nil
	}
	for _, mode := range []pvp.Mode{pvp.Mirror, pvp.Colosseum} {
		var current pvp.Season
		now := uint64(time.Now().UnixMilli())
		for _, season := range p.design.pvp.Seasons {
			if season.Mode == mode && season.Start <= now && (current.ID == 0 || season.Start > current.Start) {
				current = season
			}
		}
		if current.ID == 0 {
			continue
		}
		rows, err := p.shared.pvp.Ranking(pvp.RankQuery{Mode: mode, Season: current.ID, OwnerIndex: owner, Limit: 1})
		if err != nil {
			return err
		}
		if mode == pvp.Mirror {
			snapshot.PvpSeason = current.ID
		} else {
			snapshot.ColosseumSeason = current.ID
		}
		if len(rows) == 0 {
			continue
		}
		row := rows[0]
		if mode == pvp.Mirror {
			snapshot.PvpVP, snapshot.PvpRank = row.VP, row.Rank
		} else {
			snapshot.ColosseumVP, snapshot.ColosseumRank, snapshot.ColosseumTopPercent = row.VP, row.Rank, row.TopPercent
		}
	}
	for _, schedule := range p.options.calendars.RegularService.Contents {
		if schedule.ID != 6 {
			continue
		}
		guild, _, err := wire.Varint(snapshot.GuildBase, 1)
		if err != nil {
			return err
		}
		if guild == 0 {
			break
		}
		rows, err := p.shared.guild.Standings(context.Background(), schedule.Current.ID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Guild.ID == guild {
				snapshot.GuildRaidRank, snapshot.GuildRaidScore = row.Rank, row.Score
				break
			}
		}
	}
	if owner != 0 && p.shared.huntRanks != nil {
		season := p.monsterHuntService.CurrentSeason()
		if entry, found := p.shared.huntRanks.OwnerRank(season, owner); found {
			snapshot.MonsterHuntRank, snapshot.MonsterHuntTopPercent = entry.Rank, entry.TopPercent
		}
	}
	return nil
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
