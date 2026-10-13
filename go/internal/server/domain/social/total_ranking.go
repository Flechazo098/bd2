package social

import (
	"bd2server/internal/server/protocol/wire"
	"context"
)

func (s *Service) RankingSnapshot(ctx context.Context, limit int) (uint64, [][]byte, [][]byte, error) {
	season, err := s.raidSeason(s.config.Now().UnixMilli())
	if err != nil {
		return 0, nil, nil, err
	}
	rows, err := s.config.Owner.Standings(ctx, season.ID)
	if err != nil {
		return 0, nil, nil, err
	}
	var out, history [][]byte
	for _, row := range rows {
		if row.Rank > 0 && row.Rank <= uint64(limit) {
			out = append(out, guildRank(row))
		}
	}
	for _, entry := range s.config.RaidSeasons {
		if entry.StartsMS <= s.config.Now().UnixMilli() {
			history = append(history, wire.AppendVarint(nil, 1, entry.ID))
		}
	}
	return season.ID, out, history, nil
}
