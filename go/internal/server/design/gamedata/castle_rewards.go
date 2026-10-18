package gamedata

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
)

type CastleChallenge struct {
	Type, Value1, Value2, Score uint64
	Reward                      Reward
}
type CastleDeck struct {
	ID         uint64
	Challenges []CastleChallenge
	Rewards    []Reward
}
type CastleTower struct{ Mode, ID, Previous, Deck uint64 }
type CastleScoring struct {
	Base, Turn, Survival, Retry uint64
	BossMultiplier              float64
}
type CastleFloor struct {
	ID, Previous, Boss uint64
	Monsters           map[uint64]uint64
	Scoring            CastleScoring
	Rewards            []Reward
}
type ScoreReward struct {
	ID     uint64
	Score  float64
	Reward Reward
}
type CastleRankReward struct {
	Rank    uint64
	Rewards []Reward
}

type CastleRewardDesign struct {
	FloorRanks, TotalRanks []CastleRankReward
	TotalWarEnemies        []TotalWarEnemy
	TotalWarPresets        *PresetDesign
	PackID                 uint64
	Towers                 map[[2]uint64]CastleTower
	Floors                 map[uint64]CastleFloor
	Decks                  map[uint64]CastleDeck
	Daily, TotalWarDaily   []ScoreReward
	Reset                  FieldResetSchedule
	Characters             map[uint64]CastleCharacter
}
type CastleCharacter struct{ Unique, Element uint64 }

func LoadCastleRewardDesign(root, version string) (*CastleRewardDesign, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	d := &CastleRewardDesign{Towers: map[[2]uint64]CastleTower{}, Floors: map[uint64]CastleFloor{}, Decks: map[uint64]CastleDeck{}}
	d.Characters = map[uint64]CastleCharacter{}
	err = castleRows(db, "CharTable", func(id uint64, raw []byte) error {
		u, e := optionalScalar(raw, 20)
		if e != nil {
			return e
		}
		element, e := optionalScalar(raw, 6)
		if e != nil {
			return e
		}
		d.Characters[id] = CastleCharacter{u, element}
		return nil
	})
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT id,ProtoBuf FROM PackTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint64
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			break
		}
		var typ uint64
		typ, err = optionalScalar(raw, 55)
		if err != nil {
			break
		}
		if typ == 4 {
			if d.PackID != 0 {
				err = fmt.Errorf("gamedata: multiple castle packs")
				break
			}
			d.PackID = id
		}
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if d.PackID == 0 {
		return nil, fmt.Errorf("gamedata: no castle pack")
	}
	pack, release, err := OpenDatabase(root, version, fmt.Sprintf("pack%d", d.PackID))
	if err != nil {
		return nil, err
	}
	defer release()
	for _, table := range []struct {
		mode uint64
		name string
	}{{16, "EvilCastleGreedTowerTable"}, {19, "EvilCastleRageTowerTable"}, {20, "EvilCastleEnvyTowerTable"}} {
		var previous uint64
		err = castleRows(db, table.name, func(id uint64, raw []byte) error {
			deck, e := optionalScalar(raw, 1)
			if e != nil {
				return e
			}
			if deck == 0 {
				return fmt.Errorf("gamedata: empty castle tower deck")
			}
			d.Towers[[2]uint64{table.mode, id}] = CastleTower{table.mode, id, previous, deck}
			previous = id
			return d.loadDeck(pack, deck)
		})
		if err != nil {
			return nil, err
		}
	}
	var previous uint64
	err = castleRows(db, "EvilCastleTable", func(id uint64, raw []byte) error {
		f := CastleFloor{ID: id, Previous: previous, Monsters: map[uint64]uint64{}}
		previous = id
		boss, e := optionalScalar(raw, 8)
		if e != nil {
			return e
		}
		f.Boss = boss
		ids, e := packedInts(raw, 18)
		if e != nil {
			return e
		}
		ids = append(ids, boss)
		for _, monster := range ids {
			if monster == 0 {
				continue
			}
			var m []byte
			if e = pack.QueryRow("SELECT ProtoBuf FROM FieldMonsterTable WHERE id=?", monster).Scan(&m); e != nil {
				return e
			}
			decks, e := packedInts(m, 2)
			if e != nil || len(decks) != 1 {
				return fmt.Errorf("gamedata: unsupported castle monster decks")
			}
			f.Monsters[monster] = decks[0]
			if e = d.loadDeck(pack, decks[0]); e != nil {
				return e
			}
		}
		timer, e := optionalScalar(raw, 22)
		if e != nil {
			return e
		}
		var timing []byte
		if e = db.QueryRow("SELECT ProtoBuf FROM EvilCastleTimeAttackTable WHERE id=?", timer).Scan(&timing); e != nil {
			return e
		}
		for n, p := range map[int]*uint64{5: &f.Scoring.Base, 10: &f.Scoring.Retry, 11: &f.Scoring.Survival, 12: &f.Scoring.Turn} {
			*p, e = optionalScalar(timing, n)
			if e != nil {
				return e
			}
		}
		f.Scoring.BossMultiplier, _, e = fixed64Double(timing, 6)
		if e != nil {
			return e
		}
		f.Rewards, e = castleRewards(raw, 4, 3, 2)
		if e != nil {
			return e
		}
		d.Floors[id] = f
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, item := range []struct {
		table                 string
		dst                   *[]ScoreReward
		score, count, id, typ int
	}{{"EvilCastleDailyRewardTable", &d.Daily, 5, 2, 3, 4}, {"TotalWarRewardTable", &d.TotalWarDaily, 6, 3, 4, 5}} {
		err = castleRows(db, item.table, func(id uint64, raw []byte) error {
			r := ScoreReward{ID: id}
			var e error
			if item.score == 6 {
				r.Score, _, e = fixed64Double(raw, item.score)
			} else {
				var score uint64
				score, e = optionalScalar(raw, item.score)
				r.Score = float64(score)
			}
			if e != nil {
				return e
			}
			r.Reward.Type, e = optionalScalar(raw, item.typ)
			if e != nil {
				return e
			}
			r.Reward.ID, e = optionalScalar(raw, item.id)
			if e != nil {
				return e
			}
			r.Reward.Count, e = optionalScalar(raw, item.count)
			if e != nil {
				return e
			}
			if math.IsNaN(r.Score) || math.IsInf(r.Score, 0) || r.Score <= 0 || r.Reward.Type == 0 || r.Reward.Count == 0 {
				return fmt.Errorf("gamedata: invalid castle daily reward")
			}
			*item.dst = append(*item.dst, r)
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Slice(*item.dst, func(i, j int) bool { return (*item.dst)[i].Score < (*item.dst)[j].Score })
	}
	for _, spec := range []struct {
		table string
		dst   *[]CastleRankReward
	}{{"EvilCastleRankTable", &d.FloorRanks}, {"EvilCastleTotalRankTable", &d.TotalRanks}} {
		err = castleRows(db, spec.table, func(_ uint64, raw []byte) error {
			rank, e := optionalScalar(raw, 6)
			if e != nil {
				return e
			}
			rewards, e := castleRewards(raw, 3, 2, 1)
			if e != nil {
				return e
			}
			if rank == 0 {
				return fmt.Errorf("gamedata: invalid castle reward rank")
			}
			*spec.dst = append(*spec.dst, CastleRankReward{rank, rewards})
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Slice(*spec.dst, func(i, j int) bool { return (*spec.dst)[i].Rank < (*spec.dst)[j].Rank })
	}
	d.Reset, err = LoadFieldResetSchedule(root, version)
	if err != nil {
		return nil, err
	}
	d.TotalWarEnemies, err = loadTotalWarEnemies(root, version)
	if err != nil {
		return nil, err
	}
	d.TotalWarPresets, err = loadTotalWarPresetDesign(root, version)
	return d, err
}

func castleRows(db *sql.DB, table string, visit func(uint64, []byte) error) error {
	rows, e := db.Query("SELECT id,ProtoBuf FROM " + table + " ORDER BY id")
	if e != nil {
		return e
	}
	type row struct {
		id  uint64
		raw []byte
	}
	var records []row
	for rows.Next() {
		var id uint64
		var raw []byte
		if e = rows.Scan(&id, &raw); e != nil {
			_ = rows.Close()
			return e
		}
		records = append(records, row{id, raw})
	}
	e = rows.Err()
	_ = rows.Close()
	if e != nil {
		return e
	}
	for _, r := range records {
		if e = visit(r.id, r.raw); e != nil {
			return fmt.Errorf("gamedata: %s/%d: %w", table, r.id, e)
		}
	}
	return nil
}
func castleRewards(raw []byte, typ, id, count int) ([]Reward, error) {
	ts, e := packedInts(raw, typ)
	if e != nil {
		return nil, e
	}
	ids, e := packedInts(raw, id)
	if e != nil {
		return nil, e
	}
	cs, e := packedInts(raw, count)
	if e != nil {
		return nil, e
	}
	if len(ts) != len(ids) || len(ts) != len(cs) {
		return nil, fmt.Errorf("gamedata: castle reward array mismatch")
	}
	var out []Reward
	for i, t := range ts {
		if t == 0 && ids[i] == 0 && cs[i] == 0 {
			continue
		}
		if t == 0 || cs[i] == 0 {
			return nil, fmt.Errorf("gamedata: invalid castle reward")
		}
		out = append(out, Reward{Type: t, ID: ids[i], Count: cs[i]})
	}
	return out, nil
}
func (d *CastleRewardDesign) loadDeck(db *sql.DB, id uint64) error {
	if _, ok := d.Decks[id]; ok {
		return nil
	}
	var raw []byte
	if e := db.QueryRow("SELECT ProtoBuf FROM BattleDeckTable WHERE id=?", id).Scan(&raw); e != nil {
		return e
	}
	deck := CastleDeck{ID: id}
	types, e := packedInts(raw, 6)
	if e != nil {
		return e
	}
	arrays := map[int][]uint64{}
	for _, n := range []int{7, 8, 9, 10, 11, 12} {
		arrays[n], e = packedInts(raw, n)
		if e != nil {
			return e
		}
		if len(arrays[n]) != 0 && len(arrays[n]) != len(types) {
			return fmt.Errorf("gamedata: castle challenge array mismatch")
		}
	}
	for i, t := range types {
		if t < 1 || t > 8 {
			return fmt.Errorf("gamedata: unknown castle challenge type")
		}
		c := CastleChallenge{Type: t}
		for n, p := range map[int]*uint64{7: &c.Value1, 8: &c.Value2, 9: &c.Reward.Count, 10: &c.Reward.ID, 11: &c.Reward.Type, 12: &c.Score} {
			if len(arrays[n]) > 0 {
				*p = arrays[n][i]
			}
		}
		deck.Challenges = append(deck.Challenges, c)
	}
	deck.Rewards, e = castleRewards(raw, 36, 34, 33)
	if e != nil {
		return e
	}
	d.Decks[id] = deck
	return nil
}
