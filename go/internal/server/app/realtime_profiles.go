package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/platform/textmoderation"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/player"
	"bd2server/internal/server/runtime/room"
)

type roomRules struct {
	players *playerRegistry
	shared  *sharedRuntime
}

func (r roomRules) CheckRoomText(ctx context.Context, p room.Principal, text string) error {
	moderator := r.players.factory.options.textModerator
	if moderator == nil {
		return errors.New("text moderation is unavailable")
	}
	err := moderator.Check(ctx, "chat", p.AccountID, text)
	if errors.Is(err, textmoderation.ErrRejected) {
		return room.ErrTextRejected
	}
	return err
}

func (r roomRules) Profile(ctx context.Context, principal room.Principal) (room.Profile, error) {
	body, err := executePlayerSystem(ctx, r.players, principal.AccountID, player.ReadRoomProfile, nil)
	if err != nil {
		return room.Profile{}, err
	}
	var result room.Profile
	if err := json.Unmarshal(body, &result); err != nil {
		return result, err
	}
	guild, err := r.shared.guild.Account(ctx, principal.AccountID)
	if err != nil {
		return result, err
	}
	result.UserInfo, _, err = wire.ReplaceVarint(result.UserInfo, 2, guild.GuildID)
	return result, err
}

func (r roomRules) ValidateSelection(profile room.Profile, selection room.Selection) (room.Selection, error) {
	if selection.Kind == room.Fishing {
		if !slices.Contains(profile.FishingMaps, uint64(selection.MapID)) {
			return selection, room.ErrUnauthorized
		}
		rules, err := r.players.factory.design.eventPlay.NativeFishingRules(uint64(selection.MapID), uint64(profile.BoatLevel), uint64(profile.BoatSkinID))
		if err != nil {
			return selection, err
		}
		selection.BoatLevel = profile.BoatLevel
		selection.BoatSkinID = profile.BoatSkinID
		selection.Capacity = rules.Capacity
		selection.LoadingLimit = rules.LoadingLimit
		selection.ReconnectWindow = rules.ReconnectWindow
		selection.Duration = rules.Duration
		return selection, nil
	}
	family := "Action"
	if selection.Kind == room.Defense {
		family = "Defense"
	} else if selection.Kind != room.Action {
		return selection, room.ErrUnauthorized
	}
	for _, event := range r.players.factory.options.calendars.Events {
		if int64(event.UID) != selection.EventScheduleID {
			continue
		}
		now := time.Now().UnixMilli()
		if now < event.Start || now >= event.End {
			return selection, errors.New("room event is outside its play window")
		}
		rules, err := r.players.factory.design.eventPlay.NativeMinigameRules(family, event.ID, uint64(selection.CharacterID), uint64(selection.MonsterID), selection.Solo)
		if err != nil {
			return selection, err
		}
		selection.Capacity = rules.Capacity
		selection.GroupID = int64(rules.GroupID)
		selection.StageID = int64(rules.StageID)
		selection.DefenseLastWave = int64(rules.LastWave)
		selection.LoadingLimit = rules.LoadingLimit
		selection.Duration = rules.Duration
		return selection, nil
	}
	return selection, errors.New("unknown room event")
}

func (r roomRules) ValidateParticipant(profile room.Profile, selection room.Selection) error {
	if selection.Kind != room.Fishing || profile.RodID <= 0 {
		return room.ErrUnauthorized
	}
	_, err := r.players.factory.design.eventPlay.NativeFishingRules(uint64(selection.MapID), uint64(selection.BoatLevel), uint64(selection.BoatSkinID))
	return err
}

func (p *playerAssembly) roomProfile(ctx command.Context) ([]byte, error) {
	owner, err := p.ProfileOwner(ctx)
	if err != nil {
		return nil, err
	}
	profile, err := p.PublicProfile(ctx, owner)
	if err != nil {
		return nil, err
	}
	user := wire.AppendVarint(nil, 1, owner)
	user = wire.AppendString(user, 4, profile.UserID)
	user = wire.AppendVarint(user, 5, profile.PortraitCostumeID)
	user = wire.AppendVarint(user, 6, profile.PortraitDesignID)
	user = wire.AppendVarint(user, 7, profile.TitleID)
	avatar, err := p.avatarService.CurrentUse(ctx)
	if err != nil {
		return nil, err
	}
	use := wire.AppendVarint(nil, 1, owner)
	use = wire.AppendVarint(use, 2, avatar.UseCharID)
	for index, value := range avatar.Parts {
		use = wire.AppendVarint(use, index+3, value)
	}
	fish := p.fishingService.Profile()
	var motions []uint64
	for _, item := range p.ownedItems.All(ctx) {
		if item.Type == 50 && item.Count > 0 {
			motions = append(motions, item.ID)
		}
	}
	return json.Marshal(room.Profile{UserInfo: user, AvatarUseInfo: use, AvatarMotions: motions, SquareUnlocked: p.worldService.CanEnterFieldPackType(ctx, 11), RodID: int64(fish.RodID), BoatLevel: int64(fish.BoatLevel), BoatSkinID: int64(fish.BoatSkinID), FishingMaps: fish.Maps})
}
