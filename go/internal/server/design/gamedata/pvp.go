package gamedata

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

type PvpDefaults struct {
	MinimumVP, StartVP, BoostMaximum, TicketCost, HistoryHours, HistoryLimit, CorrectionFactor uint64
	MatchExclude, CandidateCount                                                               uint64
	MatchRanges                                                                                []uint64
}

type PvpRank struct {
	Group, ID, VP, Ranking, WinPoint, LosePoint, StartVP, PromotionBox uint64
	ELO                                                                bool
	WinRewards, LoseRewards, SeasonRewards                             []BattleReward
}

type ColosseumDefaults struct {
	MinimumVP, DefaultChange, MaximumChange, MinimumChange, VariableChange uint64
	HistoryHours, HistoryLimit, TicketBuyType, TicketBuyID, TicketBuyCost  uint64
	MatchExclude, CandidateCount                                           uint64
	MatchRanges                                                            []uint64
}

type ColosseumRank struct {
	Group, ID  uint64
	TopPercent float64
	Rewards    []BattleReward
}

type ColosseumBless struct{ ID, Type, Points uint64 }

type PvpDesign struct {
	DailyResetSeconds   int64
	FreeMirrorTickets   uint64
	Version             string
	Mirror              PvpDefaults
	Colosseum           ColosseumDefaults
	MirrorRanks         map[uint64][]PvpRank
	MirrorSeasons       map[uint64]uint64
	ColosseumSeasons    map[uint64]uint64
	ColosseumRankGroups map[uint64]uint64
	ColosseumRanks      map[uint64][]ColosseumRank
	ColosseumRules      map[uint64]map[uint64][]uint64
	Blesses             map[uint64]ColosseumBless
	Presets             *PresetDesign
}

func pvpTableRows(db *sql.DB, table string, visit func([]byte) error) (err error) {
	rows, err := db.Query("SELECT ProtoBuf FROM " + table)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		if err := visit(raw); err != nil {
			return fmt.Errorf("gamedata: %s: %w", table, err)
		}
	}
	return rows.Err()
}

func pvpScalars(raw []byte, fields map[int]*uint64) error {
	for field, target := range fields {
		value, err := optionalScalar(raw, field)
		if err != nil {
			return err
		}
		*target = value
	}
	return nil
}

func pvpRewards(raw []byte, typ, id, count int) ([]BattleReward, error) {
	rows, err := parallelRewards(raw, typ, id, count)
	if err != nil {
		return nil, err
	}
	out := make([]BattleReward, len(rows))
	for i, r := range rows {
		out[i] = BattleReward(r)
	}
	return out, nil
}

func LoadPvpDesign(root, version string) (*PvpDesign, error) {
	db, release, err := OpenDatabase(root, version, "common")
	if err != nil {
		return nil, err
	}
	defer release()
	presets, err := loadPresetDesign(db)
	if err != nil {
		return nil, err
	}
	var defaults []byte
	if err = db.QueryRow("SELECT ProtoBuf FROM GameDefaultTable WHERE id=0").Scan(&defaults); err != nil {
		return nil, err
	}
	if err = pvpScalars(defaults, map[int]*uint64{20: &presets.BaseCount, 21: &presets.Maximum}); err != nil {
		return nil, err
	}
	if err = presets.Validate(); err != nil {
		return nil, err
	}
	reset, err := LoadHuntingAPDesign(root, version)
	if err != nil {
		return nil, err
	}
	var freeTickets uint64
	if err = pvpScalars(defaults, map[int]*uint64{77: &freeTickets}); err != nil {
		return nil, err
	}
	d := &PvpDesign{DailyResetSeconds: reset.ResetSeconds, FreeMirrorTickets: freeTickets, Version: version, Presets: presets, MirrorRanks: map[uint64][]PvpRank{}, MirrorSeasons: map[uint64]uint64{}, ColosseumSeasons: map[uint64]uint64{}, ColosseumRankGroups: map[uint64]uint64{}, ColosseumRanks: map[uint64][]ColosseumRank{}, ColosseumRules: map[uint64]map[uint64][]uint64{}, Blesses: map[uint64]ColosseumBless{}}
	loaders := []struct {
		table string
		visit func([]byte) error
	}{
		{"PvpDefaultTable", func(raw []byte) error {
			m := &d.Mirror
			if err := pvpScalars(raw, map[int]*uint64{2: &m.MinimumVP, 3: &m.BoostMaximum, 5: &m.HistoryHours, 6: &m.HistoryLimit, 16: &m.CorrectionFactor, 19: &m.MatchExclude, 23: &m.TicketCost, 25: &m.CandidateCount, 26: &m.StartVP}); err != nil {
				return err
			}
			m.MatchRanges, err = packedInts(raw, 20)
			return err
		}},
		{"PvpSeasonTable", func(raw []byte) error {
			var id, season uint64
			if err := pvpScalars(raw, map[int]*uint64{1: &id, 2: &season}); err != nil {
				return err
			}
			d.MirrorSeasons[season] = id
			return nil
		}},
		{"PvpRankTable", func(raw []byte) error {
			var r PvpRank
			var elo uint64
			if err := pvpScalars(raw, map[int]*uint64{7: &r.Group, 9: &r.ID, 10: &elo, 11: &r.LosePoint, 12: &r.PromotionBox, 13: &r.Ranking, 20: &r.StartVP, 21: &r.VP, 22: &r.WinPoint}); err != nil {
				return err
			}
			r.ELO = elo != 0
			if r.WinRewards, err = pvpRewards(raw, 6, 5, 4); err != nil {
				return err
			}
			if r.LoseRewards, err = pvpRewards(raw, 3, 2, 1); err != nil {
				return err
			}
			if r.SeasonRewards, err = pvpRewards(raw, 19, 18, 17); err != nil {
				return err
			}
			d.MirrorRanks[r.Group] = append(d.MirrorRanks[r.Group], r)
			return nil
		}},
		{"ColosseumDefaultTable", func(raw []byte) error {
			m := &d.Colosseum
			if err := pvpScalars(raw, map[int]*uint64{3: &m.MinimumVP, 4: &m.TicketBuyCost, 5: &m.TicketBuyID, 6: &m.TicketBuyType, 8: &m.HistoryHours, 9: &m.HistoryLimit, 16: &m.DefaultChange, 17: &m.MaximumChange, 18: &m.MinimumChange, 19: &m.VariableChange, 22: &m.MatchExclude, 30: &m.CandidateCount}); err != nil {
				return err
			}
			m.MatchRanges, err = packedInts(raw, 23)
			return err
		}},
		{"ColosseumSeasonTable", func(raw []byte) error {
			var id, group uint64
			if err := pvpScalars(raw, map[int]*uint64{1: &id, 2: &group}); err != nil {
				return err
			}
			d.ColosseumSeasons[id] = group
			return nil
		}},
		{"ColosseumSeasonLinkTable", func(raw []byte) error {
			var id, group uint64
			if err := pvpScalars(raw, map[int]*uint64{1: &id, 2: &group}); err != nil {
				return err
			}
			d.ColosseumRankGroups[id] = group
			return nil
		}},
		{"ColosseumRuleGroupTable", func(raw []byte) error {
			var group, kind uint64
			if err := pvpScalars(raw, map[int]*uint64{1: &group, 8: &kind}); err != nil {
				return err
			}
			values, e := packedInts(raw, 9)
			if e != nil {
				return e
			}
			if d.ColosseumRules[group] == nil {
				d.ColosseumRules[group] = map[uint64][]uint64{}
			}
			d.ColosseumRules[group][kind] = values
			return nil
		}},
		{"ColosseumRankTable", func(raw []byte) error {
			var r ColosseumRank
			if err := pvpScalars(raw, map[int]*uint64{1: &r.Group, 3: &r.ID}); err != nil {
				return err
			}
			if r.TopPercent, err = optionalDouble(raw, 10); err != nil {
				return err
			}
			if r.Rewards, err = pvpRewards(raw, 9, 8, 7); err != nil {
				return err
			}
			d.ColosseumRanks[r.Group] = append(d.ColosseumRanks[r.Group], r)
			return nil
		}},
		{"ColosseumBlessTable", func(raw []byte) error {
			var b ColosseumBless
			if err := pvpScalars(raw, map[int]*uint64{1: &b.Type, 2: &b.ID, 4: &b.Points}); err != nil {
				return err
			}
			d.Blesses[b.ID] = b
			return nil
		}},
	}
	for _, loader := range loaders {
		if err := pvpTableRows(db, loader.table, loader.visit); err != nil {
			return nil, err
		}
	}
	if d.Mirror.StartVP == 0 || d.Mirror.TicketCost == 0 || len(d.MirrorRanks) == 0 || len(d.ColosseumRules) == 0 {
		return nil, fmt.Errorf("gamedata: incomplete PVP design")
	}
	for group := range d.MirrorRanks {
		sort.Slice(d.MirrorRanks[group], func(i, j int) bool { return d.MirrorRanks[group][i].ID < d.MirrorRanks[group][j].ID })
	}
	for group := range d.ColosseumRanks {
		sort.Slice(d.ColosseumRanks[group], func(i, j int) bool {
			return d.ColosseumRanks[group][i].TopPercent < d.ColosseumRanks[group][j].TopPercent
		})
	}
	return d, nil
}

func (d *PvpDesign) MirrorGroup(season uint64) uint64 {
	var selected uint64
	group := uint64(1)
	for start, id := range d.MirrorSeasons {
		if start <= season && start >= selected {
			selected, group = start, id
		}
	}
	return group
}

func (d *PvpDesign) MirrorRank(season, vp, ranking uint64) (PvpRank, error) {
	rows := d.MirrorRanks[d.MirrorGroup(season)]
	if len(rows) == 0 {
		return PvpRank{}, fmt.Errorf("gamedata: no mirror rank group for season %d", season)
	}
	var top []PvpRank
	for _, r := range rows {
		if r.Ranking > 0 {
			top = append(top, r)
		}
	}
	if len(top) > 0 && ranking > 0 && ranking <= top[len(top)-1].Ranking && vp >= top[len(top)-1].VP {
		for _, r := range top {
			if ranking <= r.Ranking {
				return r, nil
			}
		}
	}
	for _, r := range rows {
		if r.Ranking == 0 && vp >= r.VP {
			return r, nil
		}
	}
	return rows[len(rows)-1], nil
}

func (d *PvpDesign) ColosseumRule(season, kind uint64) ([]uint64, error) {
	group, ok := d.ColosseumSeasons[season]
	if !ok {
		return nil, fmt.Errorf("gamedata: unknown colosseum season %d", season)
	}
	values, ok := d.ColosseumRules[group][kind]
	if !ok {
		if kind == 30 || kind == 31 {
			return nil, nil
		}
		return nil, fmt.Errorf("gamedata: missing colosseum rule %d", kind)
	}
	return append([]uint64(nil), values...), nil
}

func (d *PvpDesign) ColosseumRank(season uint64, percent float64) (ColosseumRank, error) {
	rows := d.ColosseumRanks[d.ColosseumRankGroups[season]]
	for _, r := range rows {
		if percent <= r.TopPercent {
			return r, nil
		}
	}
	return ColosseumRank{}, fmt.Errorf("gamedata: no colosseum rank for season %d top %g", season, percent)
}
