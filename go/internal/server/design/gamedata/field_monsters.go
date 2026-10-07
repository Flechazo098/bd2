package gamedata

import (
	"database/sql"
	"fmt"
	"math"
)

// FieldMonsterDesign keeps the regeneration group requested by MonsterInfo,
// not the unrelated event monster group used by FieldEventSpawnReward.
type FieldMonsterDesign struct {
	ID, GroupID, QuestID                                               int
	BattleDeck                                                         uint64
	BattleDecks                                                        []uint64
	LifeSeconds                                                        uint64
	RegenSeconds, ResetType, Type, FieldBuff, UseBattleSkip, CrashType uint64
	Reward                                                             Reward
}

func LoadFieldMonsters(root, version string, pack int) ([]FieldMonsterDesign, error) {
	db, release, err := OpenDatabase(root, version, fmt.Sprintf("pack%d", pack))
	if err != nil {
		return nil, err
	}
	defer release()
	return loadFieldMonsters(db)
}

func loadFieldMonsters(db *sql.DB) ([]FieldMonsterDesign, error) {
	groups := map[int]FieldMonsterDesign{}
	rows, err := db.Query("SELECT id, ProtoBuf FROM FieldMonsterRegenTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		q, e1 := packedInts(raw, 6)
		life, e2 := packedInts(raw, 3)
		if e1 != nil || e2 != nil || len(q) > 1 || len(life) > 1 {
			_ = rows.Close()
			return nil, fmt.Errorf("gamedata: malformed monster regeneration %d", id)
		}
		g := FieldMonsterDesign{GroupID: id}
		for f, dst := range map[int]*uint64{7: &g.RegenSeconds, 9: &g.ResetType} {
			v, e := optionalScalar(raw, f)
			if e != nil {
				_ = rows.Close()
				return nil, e
			}
			*dst = v
		}
		if g.ResetType > 2 {
			return nil, fmt.Errorf("gamedata: unknown monster reset type")
		}
		if len(q) > 0 {
			g.QuestID = int(q[0])
		}
		if len(life) > 0 {
			if life[0] > math.MaxInt32 {
				_ = rows.Close()
				return nil, fmt.Errorf("gamedata: invalid monster lifetime %d", id)
			}
			g.LifeSeconds = life[0]
		}
		groups[id] = g
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	rows, err = db.Query("SELECT id, ProtoBuf FROM FieldMonsterTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []FieldMonsterDesign
	for rows.Next() {
		var id int
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		group, e1 := packedInts(raw, 24)
		decks, e2 := packedInts(raw, 2)
		if e1 != nil || e2 != nil || len(group) > 1 {
			return nil, fmt.Errorf("gamedata: malformed field monster %d", id)
		}
		g := FieldMonsterDesign{}
		if len(group) > 0 && group[0] > 0 {
			var ok bool
			g, ok = groups[int(group[0])]
			if !ok {
				return nil, fmt.Errorf("gamedata: monster %d references missing regeneration %d", id, group[0])
			}
		}
		g.ID = id
		for f, dst := range map[int]*uint64{30: &g.Type, 6: &g.FieldBuff, 15: &g.CrashType, 31: &g.UseBattleSkip, 27: &g.Reward.Count, 28: &g.Reward.ID, 29: &g.Reward.Type} {
			v, e := optionalScalar(raw, f)
			if e != nil {
				return nil, e
			}
			*dst = v
		}
		if len(decks) > 0 {
			g.BattleDeck = decks[0]
		}
		g.BattleDecks = append([]uint64(nil), decks...)
		result = append(result, g)
	}
	return result, rows.Err()
}
