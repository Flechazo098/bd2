package app

import (
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/commerce/gacha"
	"bd2server/internal/server/domain/startergift"
)

func (p *playerAssembly) starterGifts(ctx command.Context) ([]startergift.Gift, error) {
	owned := map[uint64]bool{}
	for _, item := range p.ownedItems.All(ctx) {
		if item.Type == 45 && item.Count != 0 {
			owned[item.ID] = true
		}
	}
	return p.seeds.starterGifts.Resolve(startergift.Selection{
		Limited: p.design.limitedCostumes, Prestige: p.design.prestigeCatalog, Avatars: p.design.avatarProfile,
		Featured:      gacha.ActivePickupCostumes(p.design.regularGacha, p.options.calendars.GachaSeed, uint64(time.Now().UTC().UnixMilli())),
		CashAvailable: func(key gamedata.CashProductKey) bool { return p.cashService.IsAvailable(ctx, key) },
		OwnedPrestige: owned,
	})
}
