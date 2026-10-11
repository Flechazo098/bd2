package pvp

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

func (s *Service) onceRewardIDs(ctx command.Context, season uint64) ([]uint64, error) {
	entries, e := ctx.State.ListEntries("pvp", "promotion_claims")
	if e != nil {
		return nil, e
	}
	prefix := strconv.FormatUint(s.rules.Design.MirrorGroup(season), 10) + ":"
	var ids []uint64
	for key := range entries {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		id, e := strconv.ParseUint(strings.TrimPrefix(key, prefix), 10, 64)
		if e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}

func (s *Service) promotionRewards(ctx command.Context, season, vp, ranking uint64) ([]gamedata.BattleReward, []uint64, error) {
	rank, e := s.rules.Design.MirrorRank(season, vp, ranking)
	if e != nil {
		return nil, nil, e
	}
	var rewards []gamedata.BattleReward
	var ids []uint64
	for _, row := range s.rules.Design.MirrorRanks[s.rules.Design.MirrorGroup(season)] {
		if row.ID < rank.ID || row.PromotionBox == 0 {
			continue
		}
		key := fmt.Sprintf("%d:%d", row.Group, row.ID)
		_, exists, e := ctx.State.LoadEntry("pvp", "promotion_claims", key)
		if e != nil {
			return nil, nil, e
		}
		if exists {
			continue
		}
		rewards = append(rewards, gamedata.BattleReward{Type: 9, ID: row.PromotionBox, Count: 1})
		ids = append(ids, row.ID)
		if e = ctx.State.PutEntry("pvp", "promotion_claims", key, []byte("true")); e != nil {
			return nil, nil, e
		}
	}
	return rewards, ids, nil
}
