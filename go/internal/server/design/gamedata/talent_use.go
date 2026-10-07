package gamedata

import (
	"bd2server/internal/server/protocol/wire"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
)

type TalentUseRule struct {
	Group, Level, Class, Catalyst, Experience, Reset, Target, Reputation uint64
	Values                                                               []float64
}
type TalentUseCharacter struct {
	Group, MaxLevel uint64
	BannedPacks     map[int]bool
}
type TalentNPC struct {
	ID, MapID                        uint64
	Groups, Rewards, CharmCharacters []uint64
}
type TalentUseDesign struct {
	Characters     map[uint64]TalentUseCharacter
	Rules          map[[2]uint64]TalentUseRule
	Rewards        map[uint64][]Reward
	Growth         *TalentGrowthDesign
	NPCs           func(int) (map[uint64]TalentNPC, error)
	Foods          map[uint64]uint64
	ResetSchedule  FieldResetSchedule
	CharmCharacter func(uint64) (StoryCharacterDesign, error)
}

func LoadTalentUseDesign(root, version string) (*TalentUseDesign, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	d, err := loadTalentUseDesign(db)
	if err != nil {
		return nil, err
	}
	d.Growth, err = loadTalentGrowthDesign(db)
	if err != nil {
		return nil, err
	}
	d.ResetSchedule, err = LoadFieldResetSchedule(root, version)
	if err != nil {
		return nil, err
	}
	d.NPCs = func(pack int) (map[uint64]TalentNPC, error) {
		db, done, e := openPackDatabase(root, version, pack)
		if e != nil {
			return nil, e
		}
		defer done()
		return loadTalentNPCs(db)
	}
	d.CharmCharacter = func(id uint64) (StoryCharacterDesign, error) {
		db, done, e := openStatDatabase(root, version)
		if e != nil {
			return StoryCharacterDesign{}, e
		}
		defer done()
		var raw []byte
		if e = db.QueryRow("SELECT ProtoBuf FROM CharTable WHERE id=?", id).Scan(&raw); e != nil {
			return StoryCharacterDesign{}, e
		}
		costume, e := requiredScalar(raw, 5)
		if e != nil {
			return StoryCharacterDesign{}, e
		}
		base, e := CharacterBaseStats(root, version, int(id), 1)
		if e != nil {
			return StoryCharacterDesign{}, e
		}
		return StoryCharacterDesign{CharacterID: id, Level: 1, CostumeID: costume, HP: uint64(base.Health)}, nil
	}
	return d, nil
}
func loadTalentUseDesign(db *sql.DB) (*TalentUseDesign, error) {
	d := &TalentUseDesign{Characters: map[uint64]TalentUseCharacter{}, Rules: map[[2]uint64]TalentUseRule{}, Rewards: map[uint64][]Reward{}}
	d.Foods = map[uint64]uint64{}
	rows, err := db.Query("SELECT id,ProtoBuf FROM TalentTable")
	if err != nil {
		return nil, err
	}
	talents := map[uint64]TalentUseCharacter{}
	for rows.Next() {
		var id uint64
		var b []byte
		if err = rows.Scan(&id, &b); err != nil {
			_ = rows.Close()
			return nil, err
		}
		g, e := optionalScalar(b, 18)
		if e != nil {
			_ = rows.Close()
			return nil, e
		}
		max, e := optionalScalar(b, 11)
		if e != nil {
			_ = rows.Close()
			return nil, e
		}
		bans, e := packedInts(b, 1)
		if e != nil {
			_ = rows.Close()
			return nil, e
		}
		v := TalentUseCharacter{Group: g, MaxLevel: max, BannedPacks: map[int]bool{}}
		for _, p := range bans {
			v.BannedPacks[int(p)] = true
		}
		talents[id] = v
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	rows, err = db.Query("SELECT id,ProtoBuf FROM CharTable")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint64
		var b []byte
		if err = rows.Scan(&id, &b); err != nil {
			_ = rows.Close()
			return nil, err
		}
		t, e := optionalScalar(b, 18)
		if e != nil {
			_ = rows.Close()
			return nil, e
		}
		if t > 0 {
			v, ok := talents[t]
			if !ok {
				_ = rows.Close()
				return nil, fmt.Errorf("gamedata: unknown character talent %d", t)
			}
			d.Characters[id] = v
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	rows, err = db.Query("SELECT groupId,id,ProtoBuf FROM TalentSkillTable")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r TalentUseRule
		var b []byte
		if err = rows.Scan(&r.Group, &r.Level, &b); err != nil {
			_ = rows.Close()
			return nil, err
		}
		for f, p := range map[int]*uint64{1: &r.Catalyst, 2: &r.Class, 5: &r.Experience, 8: &r.Reset, 12: &r.Reputation, 13: &r.Target} {
			*p, err = optionalScalar(b, f)
			if err != nil || *p > math.MaxInt32 {
				_ = rows.Close()
				return nil, fmt.Errorf("gamedata: invalid talent skill scalar")
			}
		}
		err = wire.Walk(b, func(f wire.Field) error {
			if f.Number != 14 {
				return nil
			}
			if f.Type == 5 {
				v := float64(math.Float32frombits(binary.LittleEndian.Uint32(f.Value)))
				r.Values = append(r.Values, v)
			} else if f.Type == 2 && len(f.Value)%4 == 0 {
				for raw := f.Value; len(raw) > 0; raw = raw[4:] {
					r.Values = append(r.Values, float64(math.Float32frombits(binary.LittleEndian.Uint32(raw))))
				}
			} else {
				return fmt.Errorf("gamedata: malformed talent values")
			}
			return nil
		})
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		for _, v := range r.Values {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > math.MaxInt32 {
				_ = rows.Close()
				return nil, fmt.Errorf("gamedata: invalid talent value")
			}
		}
		if r.Group == 0 || r.Level == 0 || r.Class == 0 || r.Class > 20 {
			_ = rows.Close()
			return nil, fmt.Errorf("gamedata: invalid talent identity")
		}
		d.Rules[[2]uint64{r.Group, r.Level}] = r
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	rows, err = db.Query("SELECT id,ProtoBuf FROM TalentRewardTable")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id uint64
		var b []byte
		if err = rows.Scan(&id, &b); err != nil {
			return nil, err
		}
		rewards, e := monsterHuntRewardArrays(b, 4, 3, 2)
		if e != nil {
			return nil, e
		}
		for _, r := range rewards {
			d.Rewards[id] = append(d.Rewards[id], Reward{r.Type, r.ID, r.Count}) //nolint:staticcheck // S1016
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()
	buffs := map[uint64]uint64{}
	rows, err = db.Query("SELECT id,ProtoBuf FROM FoodBuffTable")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint64
		var b []byte
		if err = rows.Scan(&id, &b); err != nil {
			_ = rows.Close()
			return nil, err
		}
		t, e := optionalScalar(b, 2)
		if e != nil {
			_ = rows.Close()
			return nil, e
		}
		g, e := optionalScalar(b, 3)
		if e != nil {
			_ = rows.Close()
			return nil, e
		}
		if t == 1 {
			buffs[id] = g
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	rows, err = db.Query("SELECT id,ProtoBuf FROM FoodTable")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id uint64
		var b []byte
		if err = rows.Scan(&id, &b); err != nil {
			return nil, err
		}
		buff, e := optionalScalar(b, 3)
		if e != nil {
			return nil, e
		}
		if g, ok := buffs[buff]; ok {
			d.Foods[id] = g
		}
	}
	return d, rows.Err()
}
func loadTalentNPCs(db *sql.DB) (map[uint64]TalentNPC, error) {
	result := map[uint64]TalentNPC{}
	rows, err := db.Query("SELECT id,ProtoBuf FROM FieldNpcTable")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var v TalentNPC
		var b []byte
		if err = rows.Scan(&v.ID, &b); err != nil {
			return nil, err
		}
		v.MapID, err = optionalScalar(b, 14)
		if err != nil {
			return nil, err
		}
		v.Groups, err = packedInts(b, 28)
		if err != nil {
			return nil, err
		}
		v.Rewards, err = packedInts(b, 29)
		if err != nil {
			return nil, err
		}
		v.CharmCharacters, err = packedInts(b, 26)
		if err != nil {
			return nil, err
		}
		if len(v.Groups) != len(v.Rewards) {
			return nil, fmt.Errorf("gamedata: mismatched NPC talent rewards")
		}
		result[v.ID] = v
	}
	return result, rows.Err()
}
