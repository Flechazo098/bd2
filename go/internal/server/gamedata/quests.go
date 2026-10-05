package gamedata

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// QuestDesign is the authoritative reward definition from QuestTable<pack>.
// Reward slot zero is normal story difficulty; later slots are separate
// difficulty tiers and must never be added to the same clear.
type QuestDesign struct {
	ID                int
	Type              int
	PriorQuestID      int
	NextQuestID       int
	GiveQuestItemIDs  []uint64
	CollectionRewards []Reward
	Rewards           [5][]Reward
}

func LoadQuestDesign(root, version string, packID int) (map[int]QuestDesign, error) {
	if packID <= 0 {
		return nil, fmt.Errorf("gamedata: invalid quest pack %d", packID)
	}
	db, release, err := OpenDatabase(root, version, "common")
	if err != nil {
		return nil, err
	}
	defer release()
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
	type questRow struct {
		id    int
		proto []byte
	}
	var questRows []questRow
	for rows.Next() {
		var row questRow
		if err := rows.Scan(&row.id, &row.proto); err != nil {
			return nil, err
		}
		questRows = append(questRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// Collection lookups reuse the database's single connection after the
	// quest result set is closed.
	result := make(map[int]QuestDesign)
	for _, row := range questRows {
		id, proto := row.id, row.proto
		if id <= 0 {
			continue
		}
		entry := QuestDesign{ID: id}
		collectionIDs, err := packedInts(proto, 6)
		if err != nil {
			return nil, fmt.Errorf("gamedata: quest%d collections: %w", id, err)
		}
		for _, collectionID := range collectionIDs {
			var collection []byte
			if err := db.QueryRow("SELECT ProtoBuf FROM CollectionTable WHERE id=?", collectionID).Scan(&collection); err != nil {
				return nil, fmt.Errorf("gamedata: quest%d collection%d: %w", id, collectionID, err)
			}
			itemID, err := requiredScalar(collection, 3)
			if err != nil {
				return nil, err
			}
			if itemID != collectionID {
				return nil, fmt.Errorf("gamedata: quest%d collection%d identity mismatch%d", id, collectionID, itemID)
			}
			entry.CollectionRewards = append(entry.CollectionRewards, Reward{Type: 17, ID: collectionID, Count: 1})
		}
		entry.GiveQuestItemIDs, err = packedInts(proto, 29)
		if err != nil {
			return nil, fmt.Errorf("gamedata: quest%d give items: %w", id, err)
		}
		for field, dst := range map[int]*int{63: &entry.Type, 37: &entry.PriorQuestID, 35: &entry.NextQuestID} {
			values, err := packedInts(proto, field)
			if err != nil || len(values) > 1 || (len(values) == 1 && values[0] > uint64(^uint32(0)>>1)) {
				return nil, fmt.Errorf("gamedata: invalid quest %d field%d", id, field)
			}
			if len(values) == 1 {
				*dst = int(values[0])
			}
		}
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
