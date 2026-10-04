package gamedata

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"bd2server/internal/server/wire"
)

// BattlePhase identifies a row in the pack's PhaseBattleTable. ID is the
// design row ID sent as current_phase_id, not a zero-based list offset.
type BattlePhase struct{ GroupID, ID, DeckID uint64 }

// BattleDeckPhases resolves a monster's phase group and verifies that the
// selected deck belongs to it. Ordinary monsters have no phase group.
func BattleDeckPhases(root, version string, packID int, monsterID, deckID uint64) ([]BattlePhase, error) {
	if packID <= 0 || monsterID == 0 || deckID == 0 || monsterID > 2147483647 || deckID > 2147483647 {
		return nil, fmt.Errorf("gamedata: invalid phase battle pack/monster/deck %d/%d/%d", packID, monsterID, deckID)
	}
	plain, err := ReadDatabase(root, version, fmt.Sprintf("pack%d", packID))
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-phase-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "pack.db")
	if err := os.WriteFile(path, plain, 0600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return battleDeckPhasesFromDB(db, monsterID, deckID)
}

func battleDeckPhasesFromDB(db *sql.DB, monsterID, deckID uint64) ([]BattlePhase, error) {
	var monster []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM FieldMonsterTable WHERE id=?", monsterID).Scan(&monster); err != nil {
		return nil, fmt.Errorf("gamedata: phase monster %d: %w", monsterID, err)
	}
	group, err := phaseScalar(monster, 21)
	if err != nil {
		return nil, err
	}
	if group == 0 {
		return nil, nil
	}
	if group > 2147483647 {
		return nil, fmt.Errorf("gamedata: phase group exceeds int32")
	}
	rows, err := db.Query("SELECT id, ProtoBuf FROM PhaseBattleTable WHERE groupId=? ORDER BY id", group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var phases []BattlePhase
	selected := false
	for rows.Next() {
		var data []byte
		var sqlID uint64
		if err := rows.Scan(&sqlID, &data); err != nil {
			return nil, err
		}
		rowGroup, err := phaseScalar(data, 3)
		if err != nil {
			return nil, err
		}
		id, err := phaseScalar(data, 4)
		if err != nil {
			return nil, err
		}
		deck, err := phaseScalar(data, 2)
		if err != nil {
			return nil, err
		}
		if rowGroup != group || id == 0 || deck == 0 || id > 2147483647 || deck > 2147483647 || id != sqlID || (len(phases) > 0 && phases[len(phases)-1].ID >= id) {
			return nil, fmt.Errorf("gamedata: invalid phase group %d row %d deck %d", rowGroup, id, deck)
		}
		phases = append(phases, BattlePhase{GroupID: group, ID: id, DeckID: deck})
		selected = selected || deck == deckID
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(phases) != 0 && !selected {
		// Phase rows name the normal decks. Hard story battles use the
		// independently authored difficulty decks for every phase.
		var data []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM BattleDeckTable WHERE id=?", deckID).Scan(&data); err != nil {
			return nil, fmt.Errorf("gamedata: selected phase deck %d: %w", deckID, err)
		}
		difficulty, err := phaseScalar(data, 24)
		if err != nil {
			return nil, err
		}
		if difficulty == 0 {
			return nil, fmt.Errorf("gamedata: phase group %d does not contain selected deck %d", group, deckID)
		}
		for i := range phases {
			mapped, err := battleDeckForDifficultyFromDB(db, phases[i].DeckID, difficulty)
			if err != nil {
				return nil, err
			}
			phases[i].DeckID = mapped
			selected = selected || mapped == deckID
		}
	}
	if len(phases) == 0 || !selected {
		return nil, fmt.Errorf("gamedata: phase group %d does not contain selected deck %d", group, deckID)
	}
	return phases, nil
}

func phaseScalar(data []byte, number int) (uint64, error) {
	var value uint64
	err := wire.Walk(data, func(f wire.Field) error {
		if f.Number != number {
			return nil
		}
		if f.Type != 0 {
			return fmt.Errorf("gamedata: phase field %d has wire type %d", number, f.Type)
		}
		values, err := packedInts(data, number)
		if err != nil {
			return err
		}
		if len(values) != 1 {
			return fmt.Errorf("gamedata: phase field %d is not scalar", number)
		}
		value = values[0]
		return nil
	})
	return value, err
}
