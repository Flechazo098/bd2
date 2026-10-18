package gamedata

import (
	"fmt"
	"math"
)

type QuestTitle struct {
	ID, FirstQuest, LastQuest int
}

func loadQuestTitles(root, version string, pack int) ([]QuestTitle, error) {
	db, release, err := openPackDatabase(root, version, pack)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := db.Query("SELECT id,ProtoBuf FROM QuestTitleTable ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("gamedata: quest titles pack%d: %w", pack, err)
	}
	defer func() { _ = rows.Close() }()
	var titles []QuestTitle
	for rows.Next() {
		var id int
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var title QuestTitle
		for field, target := range map[int]*int{1: &title.LastQuest, 2: &title.FirstQuest, 3: &title.ID} {
			values, err := packedInts(raw, field)
			if err != nil || len(values) != 1 || values[0] > math.MaxInt32 {
				return nil, fmt.Errorf("gamedata: invalid quest title pack%d id%d field%d", pack, id, field)
			}
			*target = int(values[0])
		}
		if title.ID != id || id <= 0 || title.FirstQuest <= 0 || title.LastQuest < title.FirstQuest {
			return nil, fmt.Errorf("gamedata: invalid quest title pack%d id%d", pack, id)
		}
		titles = append(titles, title)
	}
	return titles, rows.Err()
}
