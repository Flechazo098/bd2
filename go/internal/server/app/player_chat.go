package app

import (
	"bd2server/internal/server/domain/chat"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"context"
)

func (p *playerAssembly) chat() error {
	p.chatService = &chat.Service{Design: p.design.chat, Owner: p.ProfileOwner, Target: func(ctx command.Context, owner uint64) (string, error) {
		profile, err := p.PublicProfile(ctx, owner)
		return profile.UserID, err
	}}
	return p.login.AttachCommunity(func(ctx command.Context) ([]byte, error) {
		auto, global, visible, err := p.chatService.ChatSettings(ctx)
		if err != nil {
			return nil, err
		}
		var out []byte
		for i, flag := range []bool{auto, global, visible} {
			if flag {
				out = wire.AppendVarint(out, i+10, 1)
			}
		}
		if p.shared != nil && p.shared.guild != nil {
			member, base, err := p.shared.guild.LoginInfo(context.Background(), ctx.AccountID)
			if err != nil {
				return nil, err
			}
			if len(member) > 0 {
				out = wire.AppendBytes(out, 6, member)
				out = wire.AppendBytes(out, 7, base)
			}
		}
		return out, nil
	})
}
