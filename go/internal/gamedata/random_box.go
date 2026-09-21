package gamedata

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// RandomBoxDesign contains only boxes whose GameData reward graph is wholly
// deterministic.  The client asks the server to open every RandomBox, but an
// offline server must not invent an outcome for weighted boxes.  Deterministic
// material boxes (including the engraving-scroll and essence boxes) are safe
// to resolve directly from the authoritative RewardGroupTable.
type RandomBoxDesign struct {
	rewards map[uint64][]BattleReward
}

// LoadRandomBoxDesign reads RandomBoxTable -> RewardGroupTable from the
// installed common GameData.  A box is deliberately omitted if its group is
// empty, weighted, malformed, cyclic, or resolves to a non-ItemDBInfo reward;
// callers then fail closed when a client tries to open it.
func LoadRandomBoxDesign(root, version string) (*RandomBoxDesign, error) {
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-random-box-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "common.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	groups := map[uint64][]BattleReward{}
	rows, err := db.Query("SELECT id, ProtoBuf FROM RewardGroupTable")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		ids, err := packedInts(raw, 5)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: random box reward group %d item IDs: %w", id, err)
		}
		types, err := packedInts(raw, 6)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: random box reward group %d item types: %w", id, err)
		}
		counts, err := packedInts(raw, 4)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: random box reward group %d item counts: %w", id, err)
		}
		ratios, err := packedInts(raw, 8)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: random box reward group %d ratios: %w", id, err)
		}
		// A single reward is deterministic.  Ratios are presentation/probability
		// metadata in that case; a multi-entry group is intentionally not used.
		if len(ids) != 1 || len(types) != 1 || len(counts) != 1 || ids[0] == 0 || !itemDBInfoRewardType(types[0]) || counts[0] == 0 || (len(ratios) != 0 && len(ratios) != 1) {
			continue
		}
		groups[id] = []BattleReward{{Type: types[0], ID: ids[0], Count: counts[0]}}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	design := &RandomBoxDesign{rewards: map[uint64][]BattleReward{}}
	rows, err = db.Query("SELECT id, ProtoBuf FROM RandomBoxTable")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var rowID uint64
		var raw []byte
		if err := rows.Scan(&rowID, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		ids, err := packedInts(raw, 4)
		if err != nil || len(ids) != 1 || ids[0] != rowID {
			rows.Close()
			return nil, fmt.Errorf("gamedata: malformed random box %d", rowID)
		}
		groupIDs, err := packedInts(raw, 9)
		if err != nil || len(groupIDs) != 1 || groupIDs[0] == 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: random box %d reward group: %w", rowID, err)
		}
		if rewards, ok := groups[groupIDs[0]]; ok {
			design.rewards[rowID] = append([]BattleReward(nil), rewards...)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return design, nil
}

// itemDBInfoRewardType is the ItemDBInfo part of DataManager.GetItemInfo for
// the static tables this server persists in Inventory. Equipment, characters,
// costumes and currency each have a different domain store/response field, so
// a RandomBox resolving to one of them is intentionally not opened here.
func itemDBInfoRewardType(itemType uint64) bool {
	switch itemType {
	case 5, 7, 8, 9, 13, 14, 17, 27, 29:
		return true
	default:
		return false
	}
}

// Open returns the exact aggregate for count opens.  It never samples a
// weighted group, which avoids silently fabricating a random result.
func (d *RandomBoxDesign) Open(boxID, count uint64) ([]BattleReward, error) {
	if d == nil || count == 0 {
		return nil, errors.New("gamedata: invalid random box open")
	}
	rewards, ok := d.rewards[boxID]
	if !ok {
		return nil, fmt.Errorf("gamedata: random box %d is not deterministic ItemDBInfo loot", boxID)
	}
	result := make([]BattleReward, len(rewards))
	for i, reward := range rewards {
		if reward.Count > ^uint64(0)/count {
			return nil, errors.New("gamedata: random box reward count overflow")
		}
		result[i] = reward
		result[i].Count *= count
	}
	return result, nil
}
