package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle/pvp"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/shared"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Colosseum costume costs (LocalText 40030034): costumes used by attackers of
// Diamond III to Champion during the previous season are ranked by usage, and
// ColosseumDefaultTable prices each rank bracket. The tally is per season.

const colosseumBattleTotal = "total"

func colosseumPopularityPrefix(season uint64) string {
	return fmt.Sprintf("colosseum:%020d:", season)
}

type colosseumCostume struct {
	ID, Count, Rank, Cost uint64
}

// colosseumCostTable is one previous-season tally; costumes absent from it use
// ColosseumDefaults.CostDefault.
type colosseumCostTable struct {
	Battles  uint64
	Costumes []colosseumCostume
}

func (t colosseumCostTable) cost(defaults gamedata.ColosseumDefaults, costume uint64) uint64 {
	for _, row := range t.Costumes {
		if row.ID == costume {
			return row.Cost
		}
	}
	return defaults.CostDefault
}

func (s *sharedRuntime) applyColosseumPopularity(tx *shared.Tx, battle pvp.BattleSettled) error {
	attacker := battle.Attacker
	if attacker.Rank == 0 {
		return nil
	}
	rank, err := s.pvpDesign.ColosseumRank(battle.Season, attacker.TopPercent)
	if err != nil {
		return err
	}
	if rank.RankGroup == 0 || rank.RankGroup > s.pvpDesign.Colosseum.PopularCostumeMinimumRankGroup {
		return nil
	}
	prefix := colosseumPopularityPrefix(battle.Season)
	used := map[uint64]bool{}
	if err = wire.Walk(attacker.Snapshot.FullDeck, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		// FullDeck field 2 is the deck character with its selected costume id.
		costume, _, err := wire.Varint(f.Value, 5)
		if err != nil || costume == 0 {
			return fmt.Errorf("invalid colosseum deck costume")
		}
		used[costume] = true
		return nil
	}); err != nil {
		return err
	}
	if len(used) == 0 {
		return fmt.Errorf("colosseum battle without deck costumes")
	}
	for costume := range used {
		if err = popularityCount(tx, "popularity_counts", prefix+fmt.Sprintf("%010d", costume), 1); err != nil {
			return err
		}
	}
	return popularityCount(tx, "popularity_counts", prefix+colosseumBattleTotal, 1)
}

// previousColosseumSeason is the latest colosseum season already ended at.
func (s *sharedRuntime) previousColosseumSeason(at uint64) (uint64, bool) {
	var best pvp.Season
	for _, season := range s.pvpSeasons {
		if season.Mode == pvp.Colosseum && season.End <= at && season.End > best.End {
			best = season
		}
	}
	return best.ID, best.ID != 0
}

func (s *sharedRuntime) colosseumCosts(tx *shared.Tx, at uint64) (colosseumCostTable, error) {
	var table colosseumCostTable
	season, found := s.previousColosseumSeason(at)
	if !found {
		return table, nil
	}
	prefix := colosseumPopularityPrefix(season)
	if err := scanPopularity(tx, "popularity_counts", prefix, func(row shared.Entry) error {
		var count uint64
		if json.Unmarshal(row.Value, &count) != nil || count == 0 {
			return fmt.Errorf("invalid colosseum popularity")
		}
		key := strings.TrimPrefix(row.Key, prefix)
		if key == colosseumBattleTotal {
			table.Battles = count
			return nil
		}
		id, err := strconv.ParseUint(key, 10, 64)
		if err != nil || id == 0 {
			return fmt.Errorf("invalid colosseum popularity key")
		}
		table.Costumes = append(table.Costumes, colosseumCostume{ID: id, Count: count})
		return nil
	}); err != nil {
		return table, err
	}
	slices.SortFunc(table.Costumes, func(a, b colosseumCostume) int {
		if a.Count != b.Count {
			if a.Count > b.Count {
				return -1
			}
			return 1
		}
		if a.ID < b.ID {
			return -1
		}
		return 1
	})
	for i := range table.Costumes {
		table.Costumes[i].Rank = uint64(i + 1)
		table.Costumes[i].Cost = s.pvpDesign.Colosseum.CostumeCost(table.Costumes[i].Rank)
	}
	return table, nil
}

// colosseumPopularityBody encodes PopularCostumeInfoResponse for PCC_COLOSSEUM:
// count_type0 is the previous season, which the client shows beside the cost.
func colosseumPopularityBody(table colosseumCostTable) []byte {
	var body []byte
	for _, row := range table.Costumes {
		b := wire.AppendVarint(nil, 1, row.ID)
		b = wire.AppendVarint(b, 2, row.Count)
		b = wire.AppendVarint(b, 5, row.Rank)
		b = wire.AppendVarint(b, 6, row.Cost)
		body = wire.AppendBytes(body, 1, b)
	}
	var total []byte
	if table.Battles != 0 {
		total = wire.AppendVarint(nil, 2, table.Battles)
	}
	return wire.AppendBytes(body, 2, total)
}
