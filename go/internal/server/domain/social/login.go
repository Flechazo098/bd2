package social

import (
	"bd2server/internal/server/protocol/wire"
	"context"
	"encoding/json"
)

func (o *Owner) LoginInfo(ctx context.Context, account string) (member, base []byte, err error) {
	err = o.store.View(ctx, func(tx Tx) error {
		a, err := readAccount(tx, account)
		if err != nil {
			return err
		}
		if a.GuildID == 0 {
			return nil
		}
		g, err := readGuild(tx, a.GuildID)
		if err != nil {
			return err
		}
		if err := projectMemberProfiles(tx, &g); err != nil {
			return err
		}
		m, found := g.Members[account]
		if !found {
			return ErrInvalid
		}
		member = MemberInfo(g, m)
		base = wire.AppendVarint(nil, 1, g.ID)
		base = wire.AppendString(base, 2, g.Name)
		base = wire.AppendVarint(base, 3, g.Icon)
		base = wire.AppendString(base, 4, g.IconColor)
		return nil
	})
	return
}

func projectMemberProfiles(tx Tx, g *Guild) error {
	for account, member := range g.Members {
		raw, found, err := tx.Get("friend.directory", numberKey(member.OwnerIndex))
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		var profile FriendProfile
		if err := json.Unmarshal(raw, &profile); err != nil {
			return err
		}
		if profile.AccountID != account {
			return ErrInvalid
		}
		member.Name = profile.Snapshot.UserID
		member.TitleID = profile.Snapshot.TitleID
		member.PortraitID = profile.Snapshot.PortraitCostumeID
		member.PortraitDesignID = profile.Snapshot.PortraitDesignID
		member.LastLoginMS = profile.LastLoginMS
		g.Members[account] = member
	}
	return nil
}
