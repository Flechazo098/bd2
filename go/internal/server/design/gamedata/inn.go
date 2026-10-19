package gamedata

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
)

type InnRule struct {
	NPCID, MapID, MapGroup, Currency, ItemCount, FreeSquadLevel, GoodDiscount uint64
}

// LoadInns reads motel NPCs from the selected pack and their shared recovery
// rules. The /40 conversion is the client's HP pricing unit, not a pack fact.
func LoadInns(root, version string, pack int) ([]InnRule, error) {
	db, release, err := openPackDatabase(root, version, pack)
	if err != nil {
		return nil, err
	}
	defer release()
	shared, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	scenes, closeScenes, err := openLogicalReadDB(root, version, "FieldObjectSceneData")
	if err != nil {
		return nil, err
	}
	defer closeScenes()
	return loadInns(db, shared, scenes, pack)
}

func loadInns(db, shared, scenes *sql.DB, pack int) ([]InnRule, error) {
	rows, err := db.Query("SELECT id,ProtoBuf FROM FieldNpcTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	var rules []InnRule
	var recoveryIDs []uint64
	for rows.Next() {
		var id uint64
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		interactions, e := packedInts(raw, 9)
		if e != nil {
			_ = rows.Close()
			return nil, e
		}
		if !slices.Contains(interactions, uint64(2)) {
			continue
		}
		recovery, e := optionalScalar(raw, 21)
		if e != nil || recovery == 0 {
			_ = rows.Close()
			return nil, fmt.Errorf("gamedata: invalid motel NPC %d", id)
		}
		rules = append(rules, InnRule{NPCID: id})
		recoveryIDs = append(recoveryIDs, recovery)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	var placed []InnRule
	for i := range rules {
		r := &rules[i]
		var raw []byte
		if err = shared.QueryRow("SELECT ProtoBuf FROM CharRecoveryTable WHERE id=?", recoveryIDs[i]).Scan(&raw); err != nil {
			return nil, err
		}
		for field, target := range map[int]*uint64{3: &r.ItemCount, 4: &r.Currency, 5: &r.FreeSquadLevel} {
			*target, err = optionalScalar(raw, field)
			if err != nil || *target > math.MaxInt32 {
				return nil, fmt.Errorf("gamedata: invalid inn recovery rule")
			}
		}
		if r.Currency != 4 || r.ItemCount == 0 {
			return nil, fmt.Errorf("gamedata: unsupported inn currency or HP rate")
		}
		placements, err := scenes.Query(fmt.Sprintf("SELECT DISTINCT sceneName FROM FieldObjectSceneData%d WHERE objectType=1 AND tableId=? ORDER BY sceneName", pack), r.NPCID)
		if err != nil {
			return nil, err
		}
		for placements.Next() {
			var scene string
			if err = placements.Scan(&scene); err != nil {
				_ = placements.Close()
				return nil, err
			}
			p := *r
			if err = shared.QueryRow("SELECT id,ProtoBuf FROM MapTable WHERE packId=? AND mapScenePath=?", pack, scene).Scan(&p.MapID, &raw); err != nil {
				_ = placements.Close()
				return nil, fmt.Errorf("gamedata: inn NPC %d scene %s: %w", r.NPCID, scene, err)
			}
			p.MapGroup, err = optionalScalar(raw, 18)
			if err != nil {
				_ = placements.Close()
				return nil, err
			}
			placed = append(placed, p)
		}
		err = placements.Err()
		_ = placements.Close()
		if err != nil {
			return nil, err
		}
	}
	for i := range placed {
		r := &placed[i]
		var raw []byte
		err = db.QueryRow("SELECT ProtoBuf FROM ReputationGroupTable WHERE id=?", r.MapGroup).Scan(&raw)
		if err == nil {
			r.GoodDiscount, err = optionalScalar(raw, 2)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if r.GoodDiscount > 100 {
			return nil, fmt.Errorf("gamedata: invalid motel discount")
		}
	}
	return placed, nil
}
