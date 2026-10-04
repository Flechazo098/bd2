package gamedata

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
)

// AchievementCounterDesign indexes the content groups to which each counter belongs.
// AchievementTable has several tiers with the same group ID; id is not unique.
type AchievementCondition struct{ Type, SubType, ParentGroup uint64 }
type AchievementCounterDesign struct {
	Groups     map[int][]int
	Conditions map[int]AchievementCondition
}

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
	conditions := map[int]AchievementCondition{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		fields := map[int]int{}
		for _, field := range []int{1, 2, 4, 9, 13} {
			values, err := packedInts(raw, field)
			if err != nil || len(values) > 1 || (len(values) == 1 && values[0] > math.MaxInt32) {
				return nil, fmt.Errorf("gamedata: invalid achievement field%d", field)
			}
			if len(values) > 0 {
				fields[field] = int(values[0])
			}
		}
		// Client condition lookup SQL filters parentGroupId=0.
		// The wire update carries no content group.

		if fields[13] != 0 {
			continue
		}
		group, content := fields[9], fields[4]
		if group <= 0 || content < 0 || content > 1 {
			return nil, fmt.Errorf("gamedata: invalid achievement group %d/%d", group, content)
		}
		condition := AchievementCondition{Type: uint64(fields[2]), SubType: uint64(fields[1]), ParentGroup: uint64(fields[13])}
		if previous, ok := conditions[group]; ok && previous != condition {
			return nil, fmt.Errorf("gamedata: achievement group condition mismatch")
		}
		conditions[group] = condition
		if sets[group] == nil {
			sets[group] = map[int]bool{}
		}
		sets[group][content] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	design := &AchievementCounterDesign{Groups: map[int][]int{}, Conditions: conditions}
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
