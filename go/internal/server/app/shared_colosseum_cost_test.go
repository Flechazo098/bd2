package app

import (
	"context"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle/pvp"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/shared"
)

// Values from GameData 20261021182514 via gamedata_db.py: ColosseumDefaultTable
// costDefault 30, rank boundaries 5/10/15/20 cost 50/45/40/35,
// popularCostumeMinRankGroupId 6; ColosseumRankTable group 1 places Champion
// (rankGroup 1) at top 0.1% and Diamond III (rankGroup 6) at top 30%, Ruby I
// (rankGroup 7) at top 35%.
func colosseumCostFixture(t *testing.T) *sharedRuntime {
	t.Helper()
	store, err := shared.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	design := &gamedata.PvpDesign{
		Colosseum:           gamedata.ColosseumDefaults{CostDefault: 30, CostRankBoundaries: [4]uint64{5, 10, 15, 20}, CostRankValues: [4]uint64{50, 45, 40, 35}, PopularCostumeMinimumRankGroup: 6},
		ColosseumRankGroups: map[uint64]uint64{47: 1, 48: 1},
		ColosseumRanks: map[uint64][]gamedata.ColosseumRank{1: {
			{Group: 1, ID: 1, RankGroup: 1, TopPercent: 0.1},
			{Group: 1, ID: 16, RankGroup: 6, TopPercent: 30},
			{Group: 1, ID: 17, RankGroup: 7, TopPercent: 35},
		}},
	}
	seasons := []pvp.Season{{Mode: pvp.Colosseum, ID: 47, Start: 1000, End: 2000}, {Mode: pvp.Colosseum, ID: 48, Start: 2000, End: 3000}, {Mode: pvp.Mirror, ID: 9, Start: 0, End: 2500}}
	return &sharedRuntime{store: store, statistics: &gamedata.CollectionStatisticsDesign{}, pvpDesign: design, pvpSeasons: seasons}
}

func colosseumAttack(season, rank uint64, percent float64, costumes ...uint64) pvp.BattleSettled {
	var deck []byte
	for _, costume := range costumes {
		deck = wire.AppendBytes(deck, 2, wire.AppendVarint(nil, 5, costume))
	}
	attacker := pvp.PublishedSnapshot{Mode: pvp.Colosseum, Season: season, Rank: rank, TopPercent: percent, Snapshot: pvp.FullSnapshot{FullDeck: deck}}
	return pvp.BattleSettled{Mode: pvp.Colosseum, Season: season, Attacker: attacker}
}

func TestColosseumCostsRankPreviousSeasonDiamondAttackers(t *testing.T) {
	s := colosseumCostFixture(t)
	battles := []pvp.BattleSettled{
		colosseumAttack(47, 1, 0.1, 101, 102),
		colosseumAttack(47, 40, 30, 102, 103),
		colosseumAttack(47, 41, 30, 102),
		colosseumAttack(47, 90, 35, 104, 104),
		colosseumAttack(47, 0, 0, 105),
		colosseumAttack(48, 1, 0.1, 106),
	}
	for i := range 20 {
		battles = append(battles, colosseumAttack(47, 2, 0.1, 200+uint64(i)))
	}
	if err := s.store.Update(context.Background(), func(tx *shared.Tx) error {
		for _, battle := range battles {
			if err := s.applyColosseumPopularity(tx, battle); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var during, after colosseumCostTable
	if err := s.store.View(context.Background(), func(tx *shared.Tx) error {
		var err error
		if during, err = s.colosseumCosts(tx, 2500); err != nil {
			return err
		}
		after, err = s.colosseumCosts(tx, 3000)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if during.Battles != 23 || len(during.Costumes) != 23 {
		t.Fatalf("season 47 tally counted ineligible or other-season battles: %+v", during)
	}
	want := map[uint64][2]uint64{102: {3, 1}, 101: {1, 2}, 103: {1, 3}, 200: {1, 4}, 201: {1, 5}, 202: {1, 6}, 216: {1, 20}, 217: {1, 21}}
	for _, row := range during.Costumes {
		if expected, checked := want[row.ID]; checked && (row.Count != expected[0] || row.Rank != expected[1]) {
			t.Fatalf("costume %d count/rank %d/%d, want %v", row.ID, row.Count, row.Rank, expected)
		}
	}
	defaults := s.pvpDesign.Colosseum
	for costume, cost := range map[uint64]uint64{102: 50, 201: 50, 202: 45, 216: 35, 217: 30, 104: 30, 105: 30} {
		if got := during.cost(defaults, costume); got != cost {
			t.Fatalf("costume %d costs %d, want %d", costume, got, cost)
		}
	}
	if after.Battles != 1 || len(after.Costumes) != 1 || after.Costumes[0].ID != 106 {
		t.Fatalf("season 48 did not become the previous season: %+v", after)
	}

	_, body, err := s.popularInfo(command.Context{}, "/PopularCostumeInfo", wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 6), time.UnixMilli(2500))
	if err != nil {
		t.Fatal(err)
	}
	var rows, totals [][]byte
	if err := wire.Walk(body, func(f wire.Field) error {
		switch f.Number {
		case 1:
			rows = append(rows, f.Value)
		case 2:
			totals = append(totals, f.Value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	field := func(raw []byte, number int) uint64 {
		v, _, _ := wire.Varint(raw, number)
		return v
	}
	if len(rows) != 23 || len(totals) != 1 || field(totals[0], 2) != 23 || field(rows[0], 1) != 102 || field(rows[0], 2) != 3 || field(rows[0], 5) != 1 || field(rows[0], 6) != 50 {
		t.Fatalf("PCC_COLOSSEUM response: %x", body)
	}
}
