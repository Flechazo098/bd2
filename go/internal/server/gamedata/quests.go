package gamedata

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// QuestDesign is the authoritative reward definition from QuestTable<pack>.
// Reward slot zero is normal story difficulty; later slots are separate
// difficulty tiers and must never be added to the same clear.
type QuestDesign struct {
	ID      int
	Rewards [5][]Reward
}

func LoadQuestDesign(root, version string, packID int) (map[int]QuestDesign, error) {
	if packID <= 0 {
		return nil, fmt.Errorf("gamedata: invalid quest pack %d", packID)
	}
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-quests-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "quests.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return loadQuestDesignDB(db, packID)
}

func loadQuestDesignDB(db *sql.DB, packID int) (map[int]QuestDesign, error) {
	if db == nil || packID <= 0 {
		return nil, fmt.Errorf("gamedata: invalid quest design configuration")
	}
	rows, err := db.Query(fmt.Sprintf("SELECT id,ProtoBuf FROM QuestTable%d ORDER BY id", packID))
	if err != nil {
		return nil, fmt.Errorf("gamedata: query QuestTable%d: %w", packID, err)
	}
	defer rows.Close()
	result := make(map[int]QuestDesign)
	for rows.Next() {
		var id int
		var proto []byte
		if err := rows.Scan(&id, &proto); err != nil {
			return nil, err
		}
		if id <= 0 {
			continue
		}
		entry := QuestDesign{ID: id}
		for slot := 0; slot < len(entry.Rewards); slot++ {
			types, err := packedInts(proto, 56+slot)
			if err != nil {
				return nil, fmt.Errorf("gamedata: quest %d reward slot %d type: %w", id, slot, err)
			}
			ids, err := packedInts(proto, 51+slot)
			if err != nil {
				return nil, fmt.Errorf("gamedata: quest %d reward slot %d id: %w", id, slot, err)
			}
			counts, err := packedInts(proto, 46+slot)
			if err != nil {
				return nil, fmt.Errorf("gamedata: quest %d reward slot %d count: %w", id, slot, err)
			}
			if len(types) != len(ids) || len(ids) != len(counts) {
				return nil, fmt.Errorf("gamedata: quest %d reward slot %d has mismatched arrays", id, slot)
			}
			for i := range types {
				// Currency has id zero; non-stackable costume/equipment rewards
				// have count zero. Both are valid GameData representations.
				if types[i] == 0 {
					return nil, fmt.Errorf("gamedata: quest %d reward slot %d has zero type", id, slot)
				}
				entry.Rewards[slot] = append(entry.Rewards[slot], Reward{Type: types[i], ID: ids[i], Count: counts[i]})
			}
		}
		result[id] = entry
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
