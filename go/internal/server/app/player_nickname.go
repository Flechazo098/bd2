package app

import (
	"fmt"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/profile"
	"bd2server/internal/server/protocol/wire"
)

func (p *playerAssembly) nickname(ctx command.Context) error {
	initial, found, err := wire.Bytes(p.login.UserInfo, 2)
	if err != nil {
		return fmt.Errorf("load initial nickname: %w", err)
	}
	if !found || len(initial) == 0 {
		return fmt.Errorf("initial nickname is missing")
	}
	p.nicknameService, err = profile.OpenNickname(ctx, p.gameplayStore, p.ownedItems, p.checkProfileText, p.design.nicknameTicketID, string(initial))
	if err != nil {
		return fmt.Errorf("load nickname: %w", err)
	}
	return p.login.AttachNickname(p.nicknameService)
}
