package gamedata

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// QuestFormation is the static party design attached to a quest. The IDs in
// this structure are GameData design IDs, not player inventory indices.
// Runtime code must resolve them against the current account before emitting
// DeckDBInfo.
type QuestFormation struct {
	QuestID          uint64
	CharGroupID      uint64
	StoryCharGroupID uint64
	DeckList         []uint64
	Characters       []QuestCharacterDesign
	StoryCostumes    []QuestCostumeDesign
}

// QuestCharacterDesign is one row from CharGroupTable. Order is the row's Id
// inside the group and Level is the story-provided level.
type QuestCharacterDesign struct {
	Order       uint64
	CharacterID uint64
	Level       uint64
	Banned      bool
}

// QuestCostumeDesign is one row from StoryCharGroupTable. UniqueCharacterID
// comes from CostumeTable.UseUniqueCharId and is useful for matching a story
// costume to its character design. Costume 996000 is the official placeholder
// used for an account-owned party slot; callers must not treat it as owned.
type QuestCostumeDesign struct {
	Order             uint64
	CostumeID         uint64
	UniqueCharacterID uint64
}

// LoadQuestFormations reads all static formation definitions for one pack
// from the authoritative shared quest database. It intentionally does not
// invent player inventory indices or positions for an empty DeckList.
func LoadQuestFormations(root, version string, packID int) (map[int]QuestFormation, error) {
	if packID <= 0 {
		return nil, fmt.Errorf("gamedata: invalid quest pack %d", packID)
	}
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp("", "bd2-quest-formation-*.sqlite")
	if err != nil {
		return nil, fmt.Errorf("gamedata: create quest database: %w", err)
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(plain); err == nil {
		err = f.Close()
	} else {
		_ = f.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("gamedata: write quest database: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(name)+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("gamedata: open quest database: %w", err)
	}
	defer db.Close()
	return loadQuestFormationsDB(db, packID)
}

func loadQuestFormationsDB(db *sql.DB, packID int) (map[int]QuestFormation, error) {
	if db == nil || packID <= 0 {
		return nil, fmt.Errorf("gamedata: invalid quest formation source")
	}
	rows, err := db.Query(fmt.Sprintf("SELECT id,ProtoBuf FROM QuestTable%d ORDER BY id", packID))
	if err != nil {
		return nil, fmt.Errorf("gamedata: query QuestTable%d formations: %w", packID, err)
	}
	defer rows.Close()

	formations := make(map[int]QuestFormation)
	charGroups := make(map[uint64][]QuestCharacterDesign)
	storyGroups := make(map[uint64][]QuestCostumeDesign)
	for rows.Next() {
		var questID int
		var proto []byte
		if err := rows.Scan(&questID, &proto); err != nil {
			return nil, fmt.Errorf("gamedata: scan QuestTable%d formation: %w", packID, err)
		}
		charGroupID, err := optionalScalar(proto, 4)
		if err != nil {
			return nil, fmt.Errorf("gamedata: quest %d char group: %w", questID, err)
		}
		deckList, err := packedInts(proto, 12)
		if err != nil {
			return nil, fmt.Errorf("gamedata: quest %d deck list: %w", questID, err)
		}
		storyGroupID, err := optionalScalar(proto, 61)
		if err != nil {
			return nil, fmt.Errorf("gamedata: quest %d story group: %w", questID, err)
		}
		formation := QuestFormation{
			QuestID:          uint64(questID),
			CharGroupID:      charGroupID,
			StoryCharGroupID: storyGroupID,
			DeckList:         append([]uint64(nil), deckList...),
		}
		if charGroupID != 0 {
			group, ok := charGroups[charGroupID]
			if !ok {
				group, err = loadCharacterGroup(db, charGroupID)
				if err != nil {
					return nil, fmt.Errorf("gamedata: quest %d: %w", questID, err)
				}
				charGroups[charGroupID] = group
			}
			formation.Characters = append([]QuestCharacterDesign(nil), group...)
		}
		if storyGroupID != 0 {
			group, ok := storyGroups[storyGroupID]
			if !ok {
				group, err = loadStoryCostumeGroup(db, storyGroupID)
				if err != nil {
					return nil, fmt.Errorf("gamedata: quest %d: %w", questID, err)
				}
				storyGroups[storyGroupID] = group
			}
			formation.StoryCostumes = append([]QuestCostumeDesign(nil), group...)
		}
		formations[questID] = formation
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("gamedata: iterate QuestTable%d formations: %w", packID, err)
	}
	return formations, nil
}

func loadCharacterGroup(db *sql.DB, groupID uint64) ([]QuestCharacterDesign, error) {
	rows, err := db.Query("SELECT id,ProtoBuf FROM CharGroupTable WHERE GroupId=? ORDER BY id", groupID)
	if err != nil {
		return nil, fmt.Errorf("query character group %d: %w", groupID, err)
	}
	defer rows.Close()
	var result []QuestCharacterDesign
	for rows.Next() {
		var rowID int
		var proto []byte
		if err := rows.Scan(&rowID, &proto); err != nil {
			return nil, err
		}
		characterID, err := requiredScalar(proto, 1)
		if err != nil {
			return nil, fmt.Errorf("character group %d row %d: %w", groupID, rowID, err)
		}
		order, err := requiredScalar(proto, 3)
		if err != nil {
			return nil, fmt.Errorf("character group %d row %d: %w", groupID, rowID, err)
		}
		banned, err := optionalScalar(proto, 4)
		if err != nil {
			return nil, fmt.Errorf("character group %d row %d banned: %w", groupID, rowID, err)
		}
		level, err := requiredScalar(proto, 5)
		if err != nil {
			return nil, fmt.Errorf("character group %d row %d level: %w", groupID, rowID, err)
		}
		result = append(result, QuestCharacterDesign{Order: order, CharacterID: characterID, Level: level, Banned: banned != 0})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("character group %d is empty", groupID)
	}
	return result, nil
}

func loadStoryCostumeGroup(db *sql.DB, groupID uint64) ([]QuestCostumeDesign, error) {
	rows, err := db.Query("SELECT id,ProtoBuf FROM StoryCharGroupTable WHERE GroupId=? ORDER BY id", groupID)
	if err != nil {
		return nil, fmt.Errorf("query story costume group %d: %w", groupID, err)
	}
	defer rows.Close()
	var result []QuestCostumeDesign
	for rows.Next() {
		var rowID int
		var proto []byte
		if err := rows.Scan(&rowID, &proto); err != nil {
			return nil, err
		}
		costumeID, err := requiredScalar(proto, 1)
		if err != nil {
			return nil, fmt.Errorf("story costume group %d row %d: %w", groupID, rowID, err)
		}
		order, err := requiredScalar(proto, 3)
		if err != nil {
			return nil, fmt.Errorf("story costume group %d row %d: %w", groupID, rowID, err)
		}
		var costumeProto []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM CostumeTable WHERE id=?", costumeID).Scan(&costumeProto); err != nil {
			return nil, fmt.Errorf("story costume %d: %w", costumeID, err)
		}
		uniqueCharacterID, err := requiredScalar(costumeProto, 27)
		if err != nil {
			return nil, fmt.Errorf("story costume %d unique character: %w", costumeID, err)
		}
		result = append(result, QuestCostumeDesign{Order: order, CostumeID: costumeID, UniqueCharacterID: uniqueCharacterID})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("story costume group %d is empty", groupID)
	}
	return result, nil
}

func optionalScalar(proto []byte, field int) (uint64, error) {
	values, err := packedInts(proto, field)
	if err != nil {
		return 0, err
	}
	if len(values) == 0 {
		return 0, nil
	}
	if len(values) != 1 {
		return 0, fmt.Errorf("field %d has %d scalar values", field, len(values))
	}
	return values[0], nil
}

func requiredScalar(proto []byte, field int) (uint64, error) {
	value, err := optionalScalar(proto, field)
	if err != nil {
		return 0, err
	}
	if value == 0 {
		return 0, fmt.Errorf("field %d is missing or zero", field)
	}
	return value, nil
}
