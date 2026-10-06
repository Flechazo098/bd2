package gamedata

import (
	"fmt"
	"math"
	"sort"
)

type AchievementLevel struct {
	ID, NeedEXP uint64
	Rewards     []Reward
}
type AchievementLevelDesign struct{ Levels []AchievementLevel }

func LoadAchievementLevelDesign(root, version string) (*AchievementLevelDesign, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	rows, err := db.Query("SELECT ProtoBuf FROM AchievementLevelTable")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	d := &AchievementLevelDesign{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		id, err := optionalScalar(raw, 1)
		if err != nil {
			return nil, err
		}
		exp, err := optionalScalar(raw, 2)
		if err != nil {
			return nil, err
		}
		rewards, err := parallelRewards(raw, 5, 4, 3)
		if err != nil {
			return nil, err
		}
		types, err := packedInts(raw, 5)
		if err != nil {
			return nil, err
		}
		if len(rewards) != len(types) {
			return nil, fmt.Errorf("gamedata: achievement level reward arrays mismatch")
		}
		d.Levels = append(d.Levels, AchievementLevel{id, exp, rewards})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(d.Levels, func(i, j int) bool { return d.Levels[i].ID < d.Levels[j].ID })
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return d, nil
}
func (d *AchievementLevelDesign) Validate() error {
	if d == nil || len(d.Levels) == 0 {
		return fmt.Errorf("gamedata: empty achievement level design")
	}
	var previous, total uint64
	for _, l := range d.Levels {
		if l.ID <= previous || l.ID > math.MaxInt32 || l.NeedEXP == 0 || l.NeedEXP > math.MaxInt32 || total > math.MaxUint64-l.NeedEXP {
			return fmt.Errorf("gamedata: invalid achievement level design")
		}
		previous = l.ID
		total += l.NeedEXP
	}
	return nil
}

// Level follows the client: the first cumulative requirement strictly above EXP.
func (d *AchievementLevelDesign) Level(exp uint64) uint64 {
	var total uint64
	for _, l := range d.Levels {
		total += l.NeedEXP
		if total > exp {
			return l.ID
		}
	}
	return d.Levels[len(d.Levels)-1].ID
}
