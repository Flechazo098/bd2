package social

import (
	"encoding/base64"
	"fmt"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func InitialRaidResume(ctx command.Context, design *gamedata.GuildDesign, mode uint64, identity string) ([]byte, error) {
	state, err := LoadRaidState(ctx.State)
	if err != nil {
		return nil, err
	}
	ticket := state.Active
	if ticket == nil {
		return nil, fmt.Errorf("guild raid: active entry unavailable")
	}
	if ticket.AccountID != ctx.AccountID || ticket.Mode != mode || ticket.EntryID != identity || design == nil {
		return nil, fmt.Errorf("guild raid: active entry mismatch (account=%t, mode=%t, identity=%t)", ticket.AccountID == ctx.AccountID, ticket.Mode == mode, ticket.EntryID == identity)
	}
	boss, ok := design.Raid.Bosses[ticket.Season.BossGroupID][ticket.Level]
	if !ok || boss.DeckID != ticket.DeckID {
		return nil, fmt.Errorf("guild raid: entered boss design unavailable")
	}
	out := wire.AppendVarint(nil, 3, ticket.Level)
	out = wire.AppendVarint(out, 4, boss.MonsterID)
	out = wire.AppendVarint(out, 17, ticket.Season.ID)
	owners := map[uint64][]byte{}
	var order []uint64
	for _, supporter := range ticket.Supporters {
		bundle, err := base64.StdEncoding.DecodeString(supporter.Bundle)
		if err != nil {
			return nil, err
		}
		out = wire.AppendBytes(out, 21, bundle)
		slot := wire.AppendVarint(nil, 1, supporter.OwnerIndex)
		slot = wire.AppendVarint(slot, 2, supporter.Slot)
		slot = wire.AppendVarint(slot, 3, supporter.CostumeID)
		slot = wire.AppendVarint(slot, 5, supporter.UseCount)
		slot = wire.AppendString(slot, 6, supporter.Bundle)
		owner, exists := owners[supporter.OwnerIndex]
		if !exists {
			owner = wire.AppendVarint(nil, 1, supporter.OwnerIndex)
			owner = wire.AppendString(owner, 2, supporter.Name)
			order = append(order, supporter.OwnerIndex)
		}
		owners[supporter.OwnerIndex] = wire.AppendBytes(owner, 9, slot)
	}
	for _, id := range order {
		out = wire.AppendBytes(out, 24, owners[id])
	}
	return out, nil
}
