package gamedata

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// PackTransition is the authoritative story edge in PackTable. It is kept
// separate from quest IDs because each story pack owns its own QuestTable.
type PackTransition struct {
	PackID     int
	NextPackID int
}

func LoadPackTransition(root, version string, packID int) (PackTransition, error) {
	if packID <= 0 {
		return PackTransition{}, fmt.Errorf("gamedata: invalid pack transition id %d", packID)
	}
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return PackTransition{}, err
	}
	dir, err := os.MkdirTemp("", "bd2-pack-transition-")
	if err != nil {
		return PackTransition{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "common.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		return PackTransition{}, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return PackTransition{}, err
	}
	defer db.Close()
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM PackTable WHERE id=?", packID).Scan(&raw); err != nil {
		return PackTransition{}, fmt.Errorf("gamedata: pack %d: %w", packID, err)
	}
	next, err := packedInts(raw, 45) // PackTable.NextPackId
	if err != nil || len(next) != 1 || next[0] == 0 {
		return PackTransition{}, fmt.Errorf("gamedata: pack %d has invalid next-pack edge", packID)
	}
	var nextPack []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM PackTable WHERE id=?", next[0]).Scan(&nextPack); err != nil {
		return PackTransition{}, fmt.Errorf("gamedata: next pack %d: %w", next[0], err)
	}
	var questCount int
	if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM QuestTable%d", next[0])).Scan(&questCount); err != nil || questCount == 0 {
		return PackTransition{}, fmt.Errorf("gamedata: next pack %d has no quests", next[0])
	}
	return PackTransition{PackID: packID, NextPackID: int(next[0])}, nil
}
