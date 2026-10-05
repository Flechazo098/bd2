package gamedata

import (
	"database/sql"
	"fmt"
)

// BattleDeckForDifficulty mirrors QuicklyQuestHelper.GetBattleDeckIdByDifficulty
// for the three story difficulties, and verifies the resulting design row.
// Already-selected difficulty decks retain their ID.
func BattleDeckForDifficulty(root, version string, packID int, deckID, difficulty uint64) (uint64, error) {
	db, cleanup, err := openPackDatabase(root, version, packID)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	return battleDeckForDifficultyFromDB(db, deckID, difficulty)
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
