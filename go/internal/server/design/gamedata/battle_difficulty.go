package gamedata

import (
	"database/sql"
	"fmt"
	"slices"
)

type QuestBattleDeck struct {
	DeckID   uint64
	QuestIDs []uint64
}

// ResolveQuestBattleDeck applies story difficulty only to monsters bound to
// main quests. Side quests use their authored deck regardless of story level.
func ResolveQuestBattleDeck(root, version string, packID int, monsterID, deckID, difficulty uint64) (QuestBattleDeck, error) {
	if monsterID == 0 || monsterID > 2147483647 || deckID == 0 || deckID > 2147483647 || difficulty > 2 {
		return QuestBattleDeck{}, fmt.Errorf("gamedata: invalid quest monster/deck/difficulty %d/%d/%d", monsterID, deckID, difficulty)
	}
	db, cleanup, err := openPackDatabase(root, version, packID)
	if err != nil {
		return QuestBattleDeck{}, err
	}
	defer cleanup()
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM FieldMonsterTable WHERE id=?", monsterID).Scan(&raw); err != nil {
		return QuestBattleDeck{}, fmt.Errorf("gamedata: quest monster %d: %w", monsterID, err)
	}
	quests, err := packedInts(raw, 22)
	if err != nil {
		return QuestBattleDeck{}, err
	}
	if len(quests) == 0 {
		return QuestBattleDeck{}, fmt.Errorf("gamedata: quest monster %d has no quest range", monsterID)
	}
	decks, err := packedInts(raw, 2)
	if err != nil {
		return QuestBattleDeck{}, err
	}
	group, err := phaseScalar(raw, 21)
	if err != nil {
		return QuestBattleDeck{}, err
	}
	common, release, err := OpenDatabase(root, version, "common")
	if err != nil {
		return QuestBattleDeck{}, err
	}
	defer release()
	var kind uint64
	for i, quest := range quests {
		if quest == 0 || quest > 2147483647 {
			return QuestBattleDeck{}, fmt.Errorf("gamedata: quest monster %d has invalid quest %d", monsterID, quest)
		}
		if err := common.QueryRow(fmt.Sprintf("SELECT ProtoBuf FROM QuestTable%d WHERE id=?", packID), quest).Scan(&raw); err != nil {
			return QuestBattleDeck{}, fmt.Errorf("gamedata: monster %d quest %d: %w", monsterID, quest, err)
		}
		questKind, err := phaseScalar(raw, 63)
		if err != nil {
			return QuestBattleDeck{}, err
		}
		if questKind > 1 || (i > 0 && questKind != kind) {
			return QuestBattleDeck{}, fmt.Errorf("gamedata: quest monster %d has unsupported or mixed quest types", monsterID)
		}
		kind = questKind
	}
	if kind == 1 {
		difficulty = 0
	}
	selected, err := battleDeckForDifficultyFromDB(db, deckID, difficulty)
	if err != nil {
		return QuestBattleDeck{}, err
	}
	if group == 0 {
		allowed := slices.Contains(decks, deckID)
		if kind == 0 && selected > difficulty*100000 {
			allowed = allowed || slices.Contains(decks, selected-difficulty*100000)
		}
		if !allowed {
			return QuestBattleDeck{}, fmt.Errorf("gamedata: quest monster %d does not contain deck %d", monsterID, deckID)
		}
	} else if _, err := battleDeckPhasesFromDB(db, monsterID, selected); err != nil {
		return QuestBattleDeck{}, err
	}
	return QuestBattleDeck{DeckID: selected, QuestIDs: quests}, nil
}

func openPackDatabase(root, version string, packID int) (*sql.DB, func(), error) {
	if packID <= 0 {
		return nil, nil, fmt.Errorf("gamedata: invalid battle pack %d", packID)
	}
	return OpenDatabase(root, version, fmt.Sprintf("pack%d", packID))
}

func battleDeckForDifficultyFromDB(db *sql.DB, deckID, difficulty uint64) (uint64, error) {
	if deckID == 0 || deckID > 2147483647 || difficulty > 2 {
		return 0, fmt.Errorf("gamedata: unsupported battle deck/difficulty %d/%d", deckID, difficulty)
	}
	var data []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM BattleDeckTable WHERE id=?", deckID).Scan(&data); err != nil {
		return 0, fmt.Errorf("gamedata: battle deck %d: %w", deckID, err)
	}
	actual, err := phaseScalar(data, 24)
	if err != nil {
		return 0, err
	}
	if actual == difficulty {
		return deckID, nil
	}
	// Only an ordinary deck can be promoted. Changing a previously selected
	// difficulty deck would conceal stale client quest state.
	if actual != 0 || difficulty == 0 {
		return 0, fmt.Errorf("gamedata: deck %d difficulty %d differs from selected %d", deckID, actual, difficulty)
	}
	selected := deckID + difficulty*100000
	if err := db.QueryRow("SELECT ProtoBuf FROM BattleDeckTable WHERE id=?", selected).Scan(&data); err != nil {
		return 0, fmt.Errorf("gamedata: difficulty deck %d: %w", selected, err)
	}
	actual, err = phaseScalar(data, 24)
	if err != nil {
		return 0, err
	}
	if actual != difficulty {
		return 0, fmt.Errorf("gamedata: difficulty deck %d has difficulty %d, want %d", selected, actual, difficulty)
	}
	return selected, nil
}
