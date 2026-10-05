package gamedata

import (
	"database/sql"
	"fmt"
)

// LoadQuestDifficulties reads the pack-specific selectable difficulties;
// normal (zero) is the base story chain and is absent from this table.
func LoadQuestDifficulties(root, version string) (map[int]map[int]bool, error) {
	db, release, err := OpenDatabase(root, version, "common")
	if err != nil {
		return nil, err
	}
	defer release()
	return loadQuestDifficultiesDB(db)
}
func loadQuestDifficultiesDB(db *sql.DB) (map[int]map[int]bool, error) {
	rows, err := db.Query("SELECT groupId,id FROM QuestDifficultyTable ORDER BY groupId,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int]map[int]bool{}
	for rows.Next() {
		var pack, level int
		if err := rows.Scan(&pack, &level); err != nil {
			return nil, err
		}
		if pack <= 0 || level < 1 || level > 4 {
			return nil, fmt.Errorf("gamedata: invalid difficulty pack%d level%d", pack, level)
		}
		if result[pack] == nil {
			result[pack] = map[int]bool{0: true}
		}
		result[pack][level] = true
	}
	return result, rows.Err()
}
