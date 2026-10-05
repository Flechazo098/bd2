package gamedata

import (
	"database/sql"
	"fmt"
	"math"
)

// FieldMonsterDesign keeps the regeneration group requested by MonsterInfo,
// not the unrelated event monster group used by FieldEventSpawnReward.
type FieldMonsterDesign struct {
	ID, GroupID, QuestID int
	BattleDeck           uint64
	LifeSeconds          uint64
}

func LoadFieldMonsters(root, version string, pack int) ([]FieldMonsterDesign, error) {
	db, release, err := OpenDatabase(root, version, fmt.Sprintf("pack%d", pack))
	if err != nil {
		return nil, err
	}
	defer release()
	return loadFieldMonsters(db)
}

func loadFieldMonsters(db *sql.DB) ([]FieldMonsterDesign, error) {
	groups := map[int]FieldMonsterDesign{}
	rows, err := db.Query("SELECT id, ProtoBuf FROM FieldMonsterRegenTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		q, e1 := packedInts(raw, 6)
		life, e2 := packedInts(raw, 3)
		if e1 != nil || e2 != nil || len(q) > 1 || len(life) > 1 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: malformed monster regeneration %d", id)
		}
		g := FieldMonsterDesign{GroupID: id}
		if len(q) > 0 {
			g.QuestID = int(q[0])
		}
		if len(life) > 0 {
			if life[0] > math.MaxInt32 {
				rows.Close()
				return nil, fmt.Errorf("gamedata: invalid monster lifetime %d", id)
			}
			g.LifeSeconds = life[0]
		}
		groups[id] = g
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	rows, err = db.Query("SELECT id, ProtoBuf FROM FieldMonsterTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []FieldMonsterDesign
	for rows.Next() {
		var id int
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		group, e1 := packedInts(raw, 24)
		decks, e2 := packedInts(raw, 2)
		if e1 != nil || e2 != nil || len(group) > 1 {
			return nil, fmt.Errorf("gamedata: malformed field monster %d", id)
		}
		if len(group) == 0 || group[0] == 0 {
			continue
		}
		g, ok := groups[int(group[0])]
		if !ok {
			return nil, fmt.Errorf("gamedata: monster %d references missing regeneration %d", id, group[0])
		}
		g.ID = id
		if len(decks) > 0 {
			g.BattleDeck = decks[0]
		}
		result = append(result, g)
	}
	return result, rows.Err()
}
