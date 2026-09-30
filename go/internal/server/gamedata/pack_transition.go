package gamedata

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// LoadQuestPackChain opens the encrypted quest database once and follows the
// authoritative PackTable.NextPackId chain. Every pack receives its own quest
// map because quest IDs are only unique inside a pack.
func LoadQuestPackChain(root, version string, startPackID int) (map[int]map[int]QuestDesign, map[int]PackTransition, error) {
	if startPackID <= 0 {
		return nil, nil, fmt.Errorf("gamedata: invalid story start pack %d", startPackID)
	}
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-pack-chain-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "common.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		return nil, nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()

	packs := make(map[int]map[int]QuestDesign)
	transitions := make(map[int]PackTransition)
	visited := make(map[int]bool)
	for packID := startPackID; packID != 0; {
		if visited[packID] {
			return nil, nil, fmt.Errorf("gamedata: cyclic next-pack edge at %d", packID)
		}
		if len(visited) >= 64 {
			return nil, nil, fmt.Errorf("gamedata: story pack chain exceeds 64 packs")
		}
		visited[packID] = true
		quests, err := loadQuestDesignDB(db, packID)
		if err != nil {
			return nil, nil, err
		}
		if len(quests) == 0 {
			return nil, nil, fmt.Errorf("gamedata: QuestTable%d is empty", packID)
		}
		packs[packID] = quests

		var raw []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM PackTable WHERE id=?", packID).Scan(&raw); err != nil {
			return nil, nil, fmt.Errorf("gamedata: pack %d: %w", packID, err)
		}
		next, err := packedInts(raw, 45)
		if err != nil || len(next) > 1 {
			return nil, nil, fmt.Errorf("gamedata: pack %d has malformed next-pack edge", packID)
		}
		nextPackID := 0
		if len(next) == 1 {
			nextPackID = int(next[0])
		}
		transitions[packID] = PackTransition{PackID: packID, NextPackID: nextPackID}
		packID = nextPackID
	}
	return packs, transitions, nil
}

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
