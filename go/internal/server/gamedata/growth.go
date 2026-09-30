package gamedata

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	_ "modernc.org/sqlite"
)

// GrowthMaterial is a stack of ResourceTable material consumed by CharGrowth.
type GrowthMaterial struct{ ID, Count uint64 }

// CharacterGrowth applies ResourceTable.MagicValue to the character's exact
// CharGrowthTable/CharLevelTable curve from the installed GameData.
func CharacterGrowth(root, version string, charID int, level, exp uint64, materials []GrowthMaterial) (uint64, uint64, []GrowthMaterial, error) {
	if charID <= 0 || level == 0 || len(materials) == 0 {
		return 0, 0, nil, fmt.Errorf("gamedata: invalid character growth input")
	}
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return 0, 0, nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-growth-")
	if err != nil {
		return 0, 0, nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "common.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		return 0, 0, nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return 0, 0, nil, err
	}
	defer db.Close()

	var charProto []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CharTable WHERE id=?", charID).Scan(&charProto); err != nil {
		return 0, 0, nil, fmt.Errorf("gamedata: character %d: %w", charID, err)
	}
	growthIDs, err := packedInts(charProto, 1)
	if err != nil || len(growthIDs) != 1 {
		return 0, 0, nil, fmt.Errorf("gamedata: character %d growth ID %v: %w", charID, growthIDs, err)
	}
	var growthProto []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CharGrowthTable WHERE id=?", growthIDs[0]).Scan(&growthProto); err != nil {
		return 0, 0, nil, err
	}
	groups, err := packedInts(growthProto, 1)
	if err != nil || len(groups) != 1 {
		return 0, 0, nil, fmt.Errorf("gamedata: growth %d group %v: %w", growthIDs[0], groups, err)
	}
	maximum, err := packedInts(growthProto, 9)
	if err != nil || len(maximum) != 1 || maximum[0] < level {
		return 0, 0, nil, fmt.Errorf("gamedata: growth %d maximum %v: %w", growthIDs[0], maximum, err)
	}

	var gained uint64
	for _, material := range materials {
		if material.ID == 0 || material.Count == 0 {
			return 0, 0, nil, fmt.Errorf("gamedata: invalid growth material")
		}
		var proto []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM ResourceTable WHERE id=?", material.ID).Scan(&proto); err != nil {
			return 0, 0, nil, fmt.Errorf("gamedata: resource %d: %w", material.ID, err)
		}
		values, err := packedInts(proto, 9)
		if err != nil || len(values) != 1 || values[0] == 0 {
			return 0, 0, nil, fmt.Errorf("gamedata: resource %d magic value %v: %w", material.ID, values, err)
		}
		if material.Count > (^uint64(0)-gained)/values[0] {
			return 0, 0, nil, fmt.Errorf("gamedata: growth experience overflow")
		}
		gained += material.Count * values[0]
	}

	curve := make(map[uint64]uint64)
	rows, err := db.Query("SELECT ProtoBuf FROM CharLevelTable")
	if err != nil {
		return 0, 0, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var proto []byte
		if err := rows.Scan(&proto); err != nil {
			return 0, 0, nil, err
		}
		group, _ := packedInts(proto, 5)
		if len(group) != 1 || group[0] != groups[0] {
			continue
		}
		levels, _ := packedInts(proto, 7)
		required, _ := packedInts(proto, 8)
		if len(levels) == 1 && len(required) == 1 {
			curve[levels[0]] = required[0]
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, nil, err
	}
	currentLevel, currentExp := level, exp
	for gained > 0 && currentLevel < maximum[0] {
		required := curve[currentLevel]
		if required == 0 || currentExp >= required {
			return 0, 0, nil, fmt.Errorf("gamedata: missing/invalid level curve group %d level %d", groups[0], currentLevel)
		}
		need := required - currentExp
		if gained < need {
			currentExp += gained
			gained = 0
			break
		}
		gained -= need
		currentLevel++
		currentExp = 0
	}
	if currentLevel == maximum[0] {
		currentExp = 0
		refunds, err := refundGrowthResources(db, gained)
		return currentLevel, currentExp, refunds, err
	}
	return currentLevel, currentExp, nil, nil
}

// Convert max-level overflow back into the installed experience resources.
// The official 2.34.13 server uses larger denominations greedily and rounds
// the final remainder up to one smallest slime.  This can refund slightly
// more nominal EXP than the overflow: the verified tutorial sample has 551
// overflow EXP and returns resource 7 x3 (600 EXP), not x2.
func refundGrowthResources(db *sql.DB, overflow uint64) ([]GrowthMaterial, error) {
	if overflow == 0 {
		return nil, nil
	}
	rows, err := db.Query("SELECT id,ProtoBuf FROM ResourceTable")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type unit struct{ id, value uint64 }
	var units []unit
	for rows.Next() {
		var id uint64
		var proto []byte
		if err := rows.Scan(&id, &proto); err != nil {
			return nil, err
		}
		kind, err := packedInts(proto, 13)
		if err != nil {
			return nil, err
		}
		value, err := packedInts(proto, 9)
		if err != nil {
			return nil, err
		}
		if len(kind) == 1 && kind[0] == 1 && len(value) == 1 && value[0] > 0 {
			units = append(units, unit{id, value[0]})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(units) == 0 {
		return nil, fmt.Errorf("gamedata: missing experience resources")
	}
	sort.Slice(units, func(i, j int) bool { return units[i].value > units[j].value })
	var refunds []GrowthMaterial
	remaining := overflow
	for i, unit := range units {
		count := remaining / unit.value
		if i == len(units)-1 && remaining%unit.value != 0 {
			count++
		}
		if count != 0 {
			refunds = append(refunds, GrowthMaterial{ID: unit.id, Count: count})
			if count > remaining/unit.value {
				remaining = 0
			} else {
				remaining -= count * unit.value
			}
		}
	}
	return refunds, nil
}
