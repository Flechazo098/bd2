package gamedata

import (
	"database/sql"
	"fmt"
	"math"
)

type MonsterHuntRewards struct{ Clear, Daily []BattleReward }
type MonsterHuntRankReward struct {
	ID, Type uint64
	Ranking  float64
	Rewards  []BattleReward
}

// MonsterHunt is the immutable common database design. Hunt decks live in
// common.db, as BattleDeckInfo.GetBattleDeckInfo(id, true) specifies.
type MonsterHunt struct {
	ID, PackID, MonsterID, DeckID, RewardGroupID, MaxLevel, RewardLevel, ChallengeableLevel uint64
	TeamOpenLevels                                                                          []uint64
	Rewards                                                                                 map[uint64]MonsterHuntRewards
	Ranks                                                                                   map[uint64][]MonsterHuntRankReward
	baseHP, healthRate, healthSlope, stage2Ratio, stage3Ratio                               float64
	stage2Level, stage3Level                                                                uint64
}

func LoadMonsterHunt(root, version string, id uint64) (*MonsterHunt, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	return loadMonsterHunt(db, id)
}
func loadMonsterHunt(db *sql.DB, id uint64) (*MonsterHunt, error) {
	if id == 0 {
		return nil, fmt.Errorf("gamedata: zero monster hunt")
	}
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM MonsterHuntTable WHERE id=?", id).Scan(&raw); err != nil {
		return nil, err
	}
	d := &MonsterHunt{Rewards: map[uint64]MonsterHuntRewards{}, Ranks: map[uint64][]MonsterHuntRankReward{}}
	for n, p := range map[int]*uint64{8: &d.ID, 18: &d.PackID, 16: &d.MonsterID, 1: &d.DeckID, 22: &d.RewardGroupID, 23: &d.RewardLevel, 14: &d.ChallengeableLevel, 26: &d.stage2Level, 28: &d.stage3Level} {
		v, e := optionalScalar(raw, n)
		if e != nil {
			return nil, e
		}
		*p = v
	}
	for n, p := range map[int]*float64{11: &d.healthRate, 12: &d.healthSlope, 27: &d.stage2Ratio, 29: &d.stage3Ratio} {
		v, _, e := fixed64Double(raw, n)
		if e != nil {
			return nil, e
		}
		*p = v
	}
	var err error
	d.TeamOpenLevels, err = packedInts(raw, 32)
	if err != nil {
		return nil, err
	}
	group, err := optionalScalar(raw, 19)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT ProtoBuf FROM MonsterHuntPartsTable WHERE groupId=? ORDER BY id", group)
	if err != nil {
		return nil, err
	}
	var body uint64
	for rows.Next() {
		var part []byte
		if err = rows.Scan(&part); err != nil {
			rows.Close()
			return nil, err
		}
		v, e := optionalScalar(part, 7)
		if e != nil {
			rows.Close()
			return nil, e
		}
		if body == 0 && v > 0 {
			body = v
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if body == 0 {
		return nil, fmt.Errorf("gamedata: hunt %d missing body", id)
	}
	var deck []byte
	if err = db.QueryRow("SELECT ProtoBuf FROM BattleDeckTable WHERE id=?", d.DeckID).Scan(&deck); err != nil {
		return nil, err
	}
	chars, err := packedInts(deck, 14)
	if err != nil {
		return nil, err
	}
	levels, err := packedInts(deck, 30)
	if err != nil {
		return nil, err
	}
	if len(chars) != len(levels) {
		return nil, fmt.Errorf("gamedata: hunt deck level arrays differ")
	}
	level := uint64(1)
	for i, c := range chars {
		if c == body {
			level = levels[i]
			break
		}
	}
	stats, err := loadCharacterStatDesign(db)
	if err != nil {
		return nil, err
	}
	base, err := stats.BaseStats(body, level)
	if err != nil {
		return nil, err
	}
	deckRate, _, err := fixed64Double(deck, 28)
	if err != nil {
		return nil, err
	}
	d.baseHP = math.Trunc(base.Health*deckRate*100) / 100
	rows, err = db.Query("SELECT level, ProtoBuf FROM MonsterHuntRewardTable WHERE groupId=? ORDER BY level", d.RewardGroupID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var lv uint64
		var r []byte
		if err = rows.Scan(&lv, &r); err != nil {
			rows.Close()
			return nil, err
		}
		clear, e := monsterHuntRewardArrays(r, 8, 7, 6)
		if e != nil {
			rows.Close()
			return nil, e
		}
		daily, e := monsterHuntRewardArrays(r, 3, 2, 1)
		if e != nil {
			rows.Close()
			return nil, e
		}
		d.Rewards[lv] = MonsterHuntRewards{clear, daily}
		if lv > d.MaxLevel {
			d.MaxLevel = lv
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT groupId,id,ProtoBuf FROM MonsterHuntRankTable ORDER BY groupId,id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var g uint64
		var r MonsterHuntRankReward
		var p []byte
		if err = rows.Scan(&g, &r.ID, &p); err != nil {
			rows.Close()
			return nil, err
		}
		r.Type, err = optionalScalar(p, 6)
		if err != nil {
			rows.Close()
			return nil, err
		}
		r.Ranking, _, err = fixed64Double(p, 5)
		if err != nil {
			rows.Close()
			return nil, err
		}
		r.Rewards, err = monsterHuntRewardArrays(p, 9, 8, 7)
		if err != nil {
			rows.Close()
			return nil, err
		}
		d.Ranks[g] = append(d.Ranks[g], r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if d.ID != id || d.PackID == 0 || d.DeckID == 0 || d.MaxLevel == 0 || d.baseHP <= 0 {
		return nil, fmt.Errorf("gamedata: invalid hunt %d design", id)
	}
	return d, nil
}

// HP matches MonsterHuntInfo.ApplyMonsterHuntStatRate, including banker
// rounding and truncation to three significant decimal digits.
func (d *MonsterHunt) HP(level uint64) (uint64, error) {
	if d == nil || level == 0 || level > d.MaxLevel {
		return 0, fmt.Errorf("gamedata: invalid hunt level %d", level)
	}
	ratio := 1.0
	if level > d.stage2Level {
		ratio = d.stage2Ratio
		if level > d.stage3Level {
			ratio = d.stage3Ratio
		}
	}
	rate := 1 + float64(level-1)*d.healthRate*.01*math.Pow(float64(level), d.healthSlope)*ratio
	hp := math.RoundToEven(d.baseHP * rate)
	if math.IsNaN(hp) || math.IsInf(hp, 0) || hp < 1 || hp >= float64(math.MaxInt64) {
		return 0, fmt.Errorf("gamedata: hunt HP overflow")
	}
	value := uint64(hp)
	scale := uint64(1)
	for value/scale >= 1000 {
		scale *= 10
	}
	return value / scale * scale, nil
}
func monsterHuntRewardArrays(raw []byte, typeField, idField, countField int) ([]BattleReward, error) {
	types, e := packedInts(raw, typeField)
	if e != nil {
		return nil, e
	}
	ids, e := packedInts(raw, idField)
	if e != nil {
		return nil, e
	}
	counts, e := packedInts(raw, countField)
	if e != nil {
		return nil, e
	}
	if len(types) != len(ids) || len(ids) != len(counts) {
		return nil, fmt.Errorf("gamedata: hunt reward arrays differ")
	}
	var result []BattleReward
	for i, t := range types {
		if t > 0 && counts[i] > 0 {
			result = append(result, BattleReward{Type: t, ID: ids[i], Count: counts[i]})
		}
	}
	return result, nil
}

func LoadMonsterHuntPresetDesign(root, version string) (*PresetDesign, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	d, err := loadPresetDesign(db)
	if err != nil {
		return nil, err
	}
	var raw []byte
	if err = db.QueryRow("SELECT ProtoBuf FROM GameDefaultTable WHERE id=0").Scan(&raw); err != nil {
		return nil, err
	}
	d.BaseCount, err = optionalScalar(raw, 84)
	if err != nil {
		return nil, err
	}
	d.Maximum, err = optionalScalar(raw, 85)
	if err != nil {
		return nil, err
	}
	return d, d.Validate()
}
