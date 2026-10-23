package pvp

import (
	"strconv"
	"strings"

	"bd2server/internal/server/domain/command"
)

// BestMirrorRankID is the best (lowest) MirrorRankTable ID the account has
// reached: every recorded promotion claim plus the current season rank once
// the account has fought this season. Zero means no rank was reached.
func (s *Service) BestMirrorRankID(ctx command.Context) (uint64, error) {
	var best uint64
	reach := func(id uint64) {
		if id != 0 && (best == 0 || id < best) {
			best = id
		}
	}
	entries, err := ctx.State.ListEntries("pvp", "promotion_claims")
	if err != nil {
		return 0, err
	}
	for key := range entries {
		_, raw, found := strings.Cut(key, ":")
		if !found {
			continue
		}
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return 0, err
		}
		reach(id)
	}
	season, err := s.current(Mirror)
	if err != nil {
		return best, nil
	}
	p, err := s.progress(Mirror, season.ID)
	if err != nil {
		return 0, err
	}
	if p.Attacks == 0 {
		return best, nil
	}
	entry, err := s.rank(Mirror, season.ID)
	if err != nil {
		return 0, err
	}
	rank, err := s.rules.Design.MirrorRank(season.ID, p.VP, entry.Rank)
	if err != nil {
		return 0, err
	}
	reach(rank.ID)
	return best, nil
}
