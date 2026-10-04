package gamedata

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
)

// AchievementCounterDesign indexes the content groups to which each counter belongs.
// AchievementTable has several tiers with the same group ID; id is not unique.
type AchievementCounterDesign struct{ Groups map[int][]int }

func LoadAchievementCounterDesign(root, version string) (*AchievementCounterDesign, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	return loadAchievementCounterDesign(db)
}

func loadAchievementCounterDesign(db *sql.DB) (*AchievementCounterDesign, error) {
	rows, err := db.Query("SELECT ProtoBuf FROM AchievementTable")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sets := map[int]map[int]bool{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		fields := map[int]int{}
		for _, field := range []int{4, 9, 13} {
			values, err := packedInts(raw, field)
			if err != nil || len(values) > 1 || (len(values) == 1 && values[0] > math.MaxInt32) {
				return nil, fmt.Errorf("gamedata: invalid achievement field%d", field)
			}
			if len(values) > 0 {
				fields[field] = int(values[0])
			}
		}
		// GetAchievementTablesByConditionType emits root groups, including roots
		// whose UI useType is disabled. The wire update carries no content group.
		if fields[13] != 0 {
			continue
		}
		group, content := fields[9], fields[4]
		if group <= 0 || content < 0 || content > 1 {
			return nil, fmt.Errorf("gamedata: invalid achievement group %d/%d", group, content)
		}
		if sets[group] == nil {
			sets[group] = map[int]bool{}
		}
		sets[group][content] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	design := &AchievementCounterDesign{Groups: map[int][]int{}}
	for group, contents := range sets {
		for content := range contents {
			design.Groups[group] = append(design.Groups[group], content)
		}
		sort.Ints(design.Groups[group])
	}
	if len(design.Groups) == 0 {
		return nil, fmt.Errorf("gamedata: empty achievement design")
	}
	return design, nil
}
