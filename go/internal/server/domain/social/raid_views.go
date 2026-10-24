package social

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"bd2server/internal/server/protocol/wire"
)

func (s *Service) raidRead(ctx context.Context, path string, p packet, a Account, g Guild, season Season, standing GuildStanding, board []GuildStanding, member RaidPersonal) ([]byte, error) {
	switch path {
	case "/GuildRaidMainInfo":
		out := wire.AppendVarint(nil, 1, season.ID)
		out = wire.AppendVarint(out, 2, member.Score)
		last := uint64(0)
		for _, r := range s.config.Owner.design.Raid.ScoreRewards {
			if standing.Score >= r.Score {
				last = r.ID
			}
		}
		out = wire.AppendVarint(out, 3, last)
		out = wire.AppendVarint(out, 4, standing.Score)
		out = wire.AppendDouble(out, 5, standing.TopPercent)
		ent, e := s.config.Owner.Entitlements(ctx, a.AccountID)
		if e != nil {
			return nil, e
		}
		if len(ent) > 0 {
			out = wire.AppendVarint(out, 6, 1)
		}
		out = wire.AppendVarint(out, 7, member.Days[s.config.Day(s.config.Now())].SupporterUses)
		rentals, e := s.config.Owner.RentalCount(ctx, g.ID, season.ID, a.AccountID)
		if e != nil {
			return nil, e
		}
		out = wire.AppendVarint(out, 8, rentals)
		if len(board) > 0 {
			out = wire.AppendVarint(out, 9, board[0].Score)
		}
		for _, v := range s.config.RaidSeasons {
			out = wire.AppendBytes(out, 10, wire.AppendVarint(nil, 1, v.ID))
		}
		for _, v := range s.config.Owner.design.Raid.FlagVersions {
			raw := wire.AppendVarint(nil, 1, v.GroupID)
			raw = wire.AppendVarint(raw, 2, v.MinSeason)
			raw = wire.AppendVarint(raw, 3, v.MaxSeason)
			out = wire.AppendBytes(out, 11, raw)
		}
		return wire.AppendVarint(out, 12, standing.Rank), nil
	case "/GuildRaidBossBattleInfo":
		out := wire.AppendVarint(nil, 1, standing.Score)
		out = wire.AppendDouble(out, 2, standing.TopPercent)
		out = wire.AppendVarint(out, 3, member.HighestLevel)
		out = wire.AppendVarint(out, 4, member.Score)
		members, e := s.config.Owner.MemberStandings(ctx, g.ID, season.ID)
		if e != nil {
			return nil, e
		}
		if len(members) > 0 {
			out = wire.AppendVarint(out, 5, members[0].OwnerIndex)
			out = wire.AppendString(out, 6, members[0].Name)
			out = wire.AppendVarint(out, 7, members[0].Score)
		}
		return out, nil
	case "/GuildRaidMemberRanking":
		target, e := p.integer(2)
		if e != nil {
			return nil, e
		}
		if target == 0 {
			target = season.ID
		}
		members, e := s.config.Owner.MemberStandings(ctx, g.ID, target)
		if e != nil {
			return nil, e
		}
		var out []byte
		for i, m := range members {
			raw := memberRank(m, uint64(i+1), g)
			out = wire.AppendBytes(out, 1, raw)
			if m.AccountID == a.AccountID {
				out = wire.AppendBytes(out, 2, raw)
			}
		}
		return out, nil
	case "/GuildRaidSeasonRanking":
		target, e := p.integer(2)
		if e != nil {
			return nil, e
		}
		if target == 0 {
			target = season.ID
		}
		rows, e := s.config.Owner.Standings(ctx, target)
		if e != nil {
			return nil, e
		}
		var out []byte
		for _, row := range rows {
			raw := guildRank(row)
			if row.Rank > 0 && row.Rank <= 100 {
				out = wire.AppendBytes(out, 1, raw)
			}
			if row.Guild.ID == g.ID {
				out = wire.AppendBytes(out, 2, raw)
			}
		}
		return out, nil
	case "/GuildRaidBossBattleHistory":
		day, e := p.integer(2)
		if e != nil {
			return nil, e
		}
		key, e := raidDay(season, day)
		if e != nil {
			return nil, e
		}
		v := member.Days[key]
		return wire.AppendVarint(wire.AppendVarint(nil, 1, v.Level), 2, v.Score), nil
	case "/GuildRaidMemberPlayHistoryInfo":
		targetSeason, e := p.integer(2)
		if e != nil {
			return nil, e
		}
		if targetSeason == 0 {
			targetSeason = season.ID
		}
		targetOwner, e := p.integer(3)
		if e != nil {
			return nil, e
		}
		account := ""
		for key, m := range g.Members {
			if m.OwnerIndex == targetOwner {
				account = key
				break
			}
		}
		if account == "" {
			members, e := s.config.Owner.MemberStandings(ctx, g.ID, targetSeason)
			if e != nil {
				return nil, e
			}
			for _, m := range members {
				if m.OwnerIndex == targetOwner {
					account = m.AccountID
					break
				}
			}
		}
		if account == "" {
			return nil, fmt.Errorf("%w: owner %d has no raid standing in season %d", ErrNotFound, targetOwner, targetSeason)
		}
		rows, e := s.config.Owner.MemberHistory(ctx, g.ID, targetSeason, account)
		if e != nil {
			return nil, e
		}
		var out []byte
		for _, row := range rows {
			out = wire.AppendBytes(out, 1, wire.AppendVarint(wire.AppendVarint(nil, 1, row.Day), 2, row.Level))
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: unknown raid view %s", ErrInvalid, path)
}
func memberRank(m MemberBest, rank uint64, g Guild) []byte {
	out := wire.AppendVarint(nil, 1, rank)
	out = wire.AppendVarint(out, 2, m.OwnerIndex)
	out = wire.AppendString(out, 3, m.Name)
	out = wire.AppendVarint(out, 4, m.Score)
	out = wire.AppendVarint(out, 5, m.PortraitID)
	out = wire.AppendVarint(out, 6, m.PortraitDesignID)
	out = wire.AppendVarint(out, 7, m.TitleID)
	return wire.AppendVarint(out, 8, m.Overkill)
}
func guildRank(v GuildStanding) []byte {
	out := wire.AppendVarint(nil, 1, v.Rank)
	out = wire.AppendVarint(out, 2, v.Score)
	out = wire.AppendVarint(out, 3, v.Guild.ID)
	out = wire.AppendString(out, 4, v.Guild.Name)
	out = wire.AppendString(out, 5, v.Guild.Message)
	out = wire.AppendVarint(out, 6, v.Guild.Icon)
	out = wire.AppendString(out, 7, v.Guild.IconColor)
	out = wire.AppendVarint(out, 9, uint64(len(v.Guild.Members)))
	return wire.AppendVarint(out, 10, v.Overkill)
}
func addMessages(out []byte, field int, rows [][]byte) []byte {
	for _, row := range rows {
		out = wire.AppendBytes(out, field, row)
	}
	return out
}
func sortedPresets(presets map[string][]byte) [][]byte {
	keys := make([]uint64, 0, len(presets))
	for key := range presets {
		if v, e := strconv.ParseUint(key, 10, 64); e == nil {
			keys = append(keys, v)
		}
	}
	slices.Sort(keys)
	var out [][]byte
	for _, key := range keys {
		out = append(out, presets[strconv.FormatUint(key, 10)])
	}
	return out
}
