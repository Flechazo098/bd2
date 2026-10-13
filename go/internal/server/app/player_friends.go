package app

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/social"
	"bd2server/internal/server/runtime/player"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (p *playerAssembly) publicFriendProfile(ctx command.Context) (social.FriendProfile, error) {
	owner, err := p.ProfileOwner(ctx)
	if err != nil {
		return social.FriendProfile{}, err
	}
	snapshot, err := p.PublicProfile(ctx, owner)
	if err != nil {
		return social.FriendProfile{}, err
	}
	profile := social.FriendProfile{AccountID: ctx.AccountID, OwnerIndex: owner, Version: uint64(time.Now().UnixNano()), LastLoginMS: time.Now().UnixMilli(), Snapshot: snapshot}
	if raw, found, err := ctx.State.LoadEntry("directory", "friend_publication", "last_login"); err != nil {
		return profile, err
	} else if found {
		if err := json.Unmarshal(raw, &profile.LastLoginMS); err != nil {
			return profile, err
		}
	}
	for _, item := range p.ownedItems.All(ctx) {
		if item.Type == 27 || item.Type == 28 {
			profile.RoomItems = append(profile.RoomItems, item)
		}
	}
	profile.RoomCostumes = p.collection.Costumes()
	profile.RoomIDs, err = (social.RoomCommerce{}).OwnedRooms(ctx)
	if err != nil {
		return profile, err
	}
	profile.SupporterDetails, err = p.profileService.PublicSupporterDetails(ctx)
	if err != nil {
		return profile, err
	}
	return profile, nil
}

func (p *playerAssembly) publishFriendProfile(ctx command.Context, login bool) error {
	if p.shared == nil || p.shared.friends == nil {
		return nil
	}
	profile, err := p.publicFriendProfile(ctx)
	if err != nil {
		return err
	}
	if login {
		profile.LastLoginMS = time.Now().UnixMilli()
		raw, err := json.Marshal(profile.LastLoginMS)
		if err != nil {
			return err
		}
		if err := ctx.State.PutEntry("directory", "friend_publication", "last_login", raw); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	return ctx.Publish(command.Event{ID: fmt.Sprintf("friend-profile:%d", profile.Version), Topic: "friend.directory", Payload: raw})
}

func (p *playerInstance) prepareFriends(ctx context.Context, request player.Command) (map[int]player.Response, error) {
	var responses map[int]player.Response
	if p.factory.shared == nil || p.factory.shared.friends == nil {
		return nil, nil
	}
	var own social.FriendProfile
	for index, item := range request.Requests {
		if !social.OwnsFriend(item.Path) {
			continue
		}
		if social.FriendMutates(item.Path) && len(request.Requests) != 1 {
			return nil, &command.GameError{PacketCode: 204, ErrorType: 9003, Message: "Friend changes require a standalone request"}
		}
		if own.OwnerIndex == 0 {
			err := p.readLocal(ctx, request.Identity, func(c command.Context) error {
				var err error
				own.OwnerIndex, err = p.assembly.ProfileOwner(c)
				return err
			})
			if err != nil {
				return nil, err
			}
			if _, err := p.factory.shared.friends.Profile(ctx, own.OwnerIndex); errors.Is(err, social.ErrNotFound) {
				if err := p.readLocal(ctx, request.Identity, func(c command.Context) error { var err error; own, err = p.assembly.publicFriendProfile(c); return err }); err != nil {
					return nil, err
				}
				if err := p.factory.shared.friends.Publish(ctx, own); err != nil {
					return nil, err
				}
			} else if err != nil {
				return nil, err
			}
		}
		if strings.HasPrefix(item.Path, "/MyRoom") {
			var current social.FriendProfile
			if err := p.readLocal(ctx, request.Identity, func(c command.Context) error {
				var err error
				current, err = p.assembly.publicFriendProfile(c)
				return err
			}); err != nil {
				return nil, err
			}
			if err := p.factory.shared.friends.Publish(ctx, current); err != nil {
				return nil, err
			}
		}
		if err := p.factory.shared.friends.PrepareRoomText(ctx, request.Identity, item.Path, item.Body, func(text string) error {
			return p.assembly.checkText(ctx, request.Identity.AccountID, "room_name", text, map[string]int{"/MyRoomNameChange": 234, "/MyRoomSave": 229, "/MyRoomPresetSave": 642, "/MyRoomPresetNameChange": 643}[item.Path])
		}); err != nil {
			return nil, err
		}
		var contentType uint64
		if item.Path == "/SupporterBattleInfo" || item.Path == "/SupporterBorrow" {
			if err := p.readLocal(ctx, request.Identity, func(c command.Context) error {
				pack, err := p.assembly.worldService.CurrentPackID(c)
				if err != nil {
					return err
				}
				typ, found := p.assembly.design.recovery.Types[pack]
				if !found {
					return social.ErrInvalid
				}
				contentType = typ
				return nil
			}); err != nil {
				return nil, err
			}
		}
		code, body, err := p.factory.shared.friends.Execute(ctx, request.Identity, own.OwnerIndex, item.Path, item.Body, contentType)
		if errors.Is(err, social.ErrConflict) {
			return nil, player.ErrIdentityConflict
		}
		if err != nil {
			return nil, err
		}
		if responses == nil {
			responses = map[int]player.Response{}
		}
		responses[index] = player.Response{PacketCode: code, Body: body}
	}
	return responses, nil
}

func friendProfileChanged(requests []player.Request) bool {
	for _, r := range requests {
		for _, prefix := range []string{"/Equip", "/CharGrowth", "/CharAwake", "/CharImprint", "/CharImmortal", "/Costume", "/Gacha", "/MercenaryScout", "/CharSpecialScout", "/PrestigeSkin"} {
			if strings.HasPrefix(r.Path, prefix) && !strings.HasSuffix(r.Path, "Info") && !strings.HasSuffix(r.Path, "InfoList") {
				return true
			}
		}
		switch r.Path {
		case "/MyRoomExpand", "/MyRoomShopBuy", "/MyRoomShopMultiBuy":
			return true
		case "/UserPortraitChange", "/UserTitleChange", "/UserNicknameChange", "/SupporterRegister", "/SupporterRemove", "/UpdateUserContentsInfoOption", "/UserGreetingChange", "/IdCardSave", "/IdCardRecovery", "/AvatarUse", "/AvatarSave", "/GuildCreate", "/GuildJoin", "/GuildLeave", "/GuildMemberBan", "/GuildInfoEdit", "/GuildNameChange", "/CharLevelUp", "/CostumeLevelUp", "/CharAwake", "/EquipUse", "/EquipUpgrade", "/CostumePotentialUpgrade", "/PrestigeSkinUse":
			return true
		}
	}
	return false
}
