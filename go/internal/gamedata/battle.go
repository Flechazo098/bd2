package gamedata

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	"bd2server/internal/wire"
	_ "modernc.org/sqlite"
)

// BattleReward is a static victory reward from BattleDeckTable in a pack DB.
// It is a design definition, not a player's owned item or server-generated ID.
type BattleReward struct {
	Type  uint64
	ID    uint64
	Count uint64
}

// BattleDeckRewards uses the enemy battle deck actually chosen by the client
// (BattleEnter field 4); a monster may have several different battle decks.
func BattleDeckRewards(root, version string, packID int, deckID uint64) ([]BattleReward, error) {
	if packID <= 0 || deckID == 0 || deckID > uint64(^uint(0)>>1) {
		return nil, fmt.Errorf("gamedata: invalid battle pack/deck %d/%d", packID, deckID)
	}
	return readBattleRewards(root, version, packID, int(deckID), 0)
}

// BattleRewards is a legacy convenience for a monster with exactly one deck.
// Call BattleDeckRewards for a real battle, where the chosen deck is known.
func BattleRewards(root, version string, packID, monsterID int) ([]BattleReward, error) {
	if packID <= 0 || monsterID <= 0 {
		return nil, fmt.Errorf("gamedata: invalid battle pack/monster %d/%d", packID, monsterID)
	}
	return readBattleRewards(root, version, packID, 0, monsterID)
}

func readBattleRewards(root, version string, packID, deckID, monsterID int) ([]BattleReward, error) {
	plain, err := ReadDatabase(root, version, fmt.Sprintf("pack%d", packID))
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-battle-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "pack.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if monsterID != 0 {
		var monster []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM FieldMonsterTable WHERE id=?", monsterID).Scan(&monster); err != nil {
			return nil, fmt.Errorf("gamedata: monster %d: %w", monsterID, err)
		}
		deckIDs, err := packedInts(monster, 2)
		if err != nil || len(deckIDs) != 1 || deckIDs[0] == 0 || deckIDs[0] > uint64(^uint(0)>>1) {
			return nil, fmt.Errorf("gamedata: monster %d requires a selected battle deck (found %v): %w", monsterID, deckIDs, err)
		}
		deckID = int(deckIDs[0])
	}
	var design []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM BattleDeckTable WHERE id=?", deckID).Scan(&design); err != nil {
		return nil, fmt.Errorf("gamedata: battle deck %d: %w", deckID, err)
	}
	types, err := packedInts(design, 36)
	if err != nil {
		return nil, err
	}
	ids, err := packedInts(design, 34)
	if err != nil {
		return nil, err
	}
	counts, err := packedInts(design, 33)
	if err != nil {
		return nil, err
	}
	if len(types) != len(ids) || len(ids) != len(counts) {
		return nil, fmt.Errorf("gamedata: battle deck %d reward arrays differ in length", deckID)
	}
	rewards := make([]BattleReward, len(types))
	for i := range rewards {
		if types[i] == 0 || ids[i] == 0 || counts[i] == 0 {
			return nil, fmt.Errorf("gamedata: invalid battle deck %d reward", deckID)
		}
		rewards[i] = BattleReward{Type: types[i], ID: ids[i], Count: counts[i]}
	}
	return rewards, nil
}

func packedInts(proto []byte, number int) ([]uint64, error) {
	var out []uint64
	err := wire.Walk(proto, func(f wire.Field) error {
		if f.Number != number {
			return nil
		}
		if f.Type != 0 && f.Type != 2 {
			return fmt.Errorf("gamedata: field %d has type %d", number, f.Type)
		}
		for data := f.Value; len(data) > 0; {
			value, n := binary.Uvarint(data)
			if n <= 0 {
				return fmt.Errorf("gamedata: invalid packed field %d", number)
			}
			out = append(out, value)
			data = data[n:]
		}
		return nil
	})
	return out, err
}
