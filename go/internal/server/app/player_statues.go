package app

import (
	"context"
	"time"

	"bd2server/internal/server/domain/battle/pvp"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/world"
	"bd2server/internal/server/protocol/wire"
)

func statuePlayer(kind, season, rank, owner uint64, name string, portrait, design uint64) []byte {
	b := wire.AppendVarint(nil, 1, kind)
	b = wire.AppendVarint(b, 2, season)
	b = wire.AppendVarint(b, 3, rank)
	b = wire.AppendVarint(b, 4, owner)
	b = wire.AppendString(b, 5, name)
	b = wire.AppendVarint(b, 6, portrait)
	return wire.AppendVarint(b, 7, design)
}
func statueGroup(kind, season, rank uint64) []byte {
	b := wire.AppendVarint(nil, 1, kind)
	b = wire.AppendVarint(b, 2, season)
	return wire.AppendVarint(b, 3, rank)
}

func (p *playerAssembly) rankStatues(ctx command.Context) ([]world.RankStatue, error) {
	if p.shared == nil {
		return nil, nil
	}
	var out []world.RankStatue
	for _, mode := range []pvp.Mode{pvp.Mirror, pvp.Colosseum} {
		season, rows, err := p.shared.pvp.StatueRanking(mode)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			continue
		}
		kind := uint64(1)
		if mode == pvp.Colosseum {
			kind = 5
		}
		g := statueGroup(kind, season, 0)
		for _, row := range rows {
			profile := row.Profile
			g = wire.AppendBytes(g, 5, statuePlayer(kind, season, row.Rank, profile.OwnerIndex, profile.Name, profile.PortraitCostume, profile.PortraitDesign))
		}
		out = append(out, world.RankStatue{ID: kind, Season: season, Groups: [][]byte{g}})
	}
	var huntSeason uint64
	for _, season := range monsterHuntSeasons(p.seeds.defaults) {
		if season.Calculate <= uint64(time.Now().UnixMilli()) && season.ID > huntSeason {
			huntSeason = season.ID
		}
	}
	if huntSeason != 0 {
		rows := p.shared.huntRanks.Top(huntSeason, 3)
		if len(rows) > 0 {
			g := statueGroup(3, huntSeason, 0)
			for _, row := range rows {
				profile := row.Profile
				g = wire.AppendBytes(g, 5, statuePlayer(3, huntSeason, row.Rank, profile.Owner, profile.Name, profile.Portrait, profile.PortraitDesign))
			}
			out = append(out, world.RankStatue{ID: 3, Season: huntSeason, Groups: [][]byte{g}})
		}
	}
	cancel := ctx.Cancellation
	if cancel == nil {
		cancel = context.Background()
	}
	season, rows, err := p.shared.guild.StatueRanking(cancel)
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 {
		statue := world.RankStatue{ID: 4, Season: season}
		for _, row := range rows {
			g := statueGroup(4, season, row.Rank)
			base := wire.AppendVarint(nil, 1, row.Guild.ID)
			base = wire.AppendString(base, 2, row.Guild.Name)
			base = wire.AppendVarint(base, 3, row.Guild.Icon)
			base = wire.AppendString(base, 4, row.Guild.IconColor)
			g = wire.AppendBytes(g, 4, base)
			for i, member := range row.Members[:min(3, len(row.Members))] {
				g = wire.AppendBytes(g, 5, statuePlayer(4, season, uint64(i+1), member.OwnerIndex, member.Name, member.PortraitID, member.PortraitDesignID))
			}
			statue.Groups = append(statue.Groups, g)
		}
		out = append(out, statue)
	}
	return out, nil
}
