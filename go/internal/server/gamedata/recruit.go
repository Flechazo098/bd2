package gamedata

import (
	"database/sql"
	"fmt"
	"math"
	"time"
)

// RecruitRule is MercenaryScoutTable. Type zero uses MercenaryScout; type
// one belongs to the separate special recruitment protocol.
type RecruitRule struct {
	ID, CostumeID, PackID, Type, TalkGroupID, AppearProb uint64
	Costs                                                []PromotionCost
}
type RecruitDesign struct {
	Rules                                                           map[uint64]RecruitRule
	Characters                                                      map[uint64]CharacterDesign
	AppearCount, AutoResetMinute, ResetCount, ResetType, ResetLimit uint64
}

func (d *RecruitDesign) Character(id uint64) (CharacterDesign, bool) {
	v, ok := d.Characters[id]
	return v, ok
}
func LoadRecruitDesign(root, version string) (RecruitDesign, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return RecruitDesign{}, err
	}
	defer closeDB()
	return loadRecruitDesign(db)
}
func loadRecruitDesign(db *sql.DB) (RecruitDesign, error) {
	d := RecruitDesign{Rules: map[uint64]RecruitRule{}}
	if db == nil {
		return d, fmt.Errorf("gamedata: nil recruit database")
	}
	err := friendshipRows(db, "SELECT id,ProtoBuf FROM MercenaryScoutTable ORDER BY id", func(id uint64, p []byte) error {
		r := RecruitRule{}
		for i, dst := range []*uint64{&r.ID, &r.CostumeID, &r.PackID, &r.Type, &r.TalkGroupID, &r.AppearProb} {
			v, e := friendshipScalar(p, []int{3, 2, 4, 9, 8, 1}[i])
			if e != nil {
				return e
			}
			*dst = v
		}
		counts, e := packedInts(p, 5)
		if e != nil {
			return e
		}
		ids, e := packedInts(p, 6)
		if e != nil {
			return e
		}
		types, e := packedInts(p, 7)
		if e != nil {
			return e
		}
		if r.ID != id || id == 0 || id > math.MaxInt32 || r.CostumeID == 0 || r.CostumeID > math.MaxInt32 || r.PackID > math.MaxInt32 || r.AppearProb > math.MaxInt32 || r.TalkGroupID > math.MaxInt32 || r.Type > 1 || len(counts) == 0 || len(counts) != len(ids) || len(ids) != len(types) {
			return fmt.Errorf("invalid recruit rule %d", id)
		}
		for i, count := range counts {
			if count == 0 || count > math.MaxInt32 || types[i] != 8 || ids[i] == 0 || ids[i] > math.MaxInt32 {
				return fmt.Errorf("unsupported recruit cost %d", id)
			}
			r.Costs = append(r.Costs, PromotionCost{Type: types[i], ID: ids[i], Count: count})
		}
		d.Rules[id] = r
		return nil
	})
	if err != nil {
		return d, fmt.Errorf("gamedata: recruitment: %w", err)
	}
	d.Characters = map[uint64]CharacterDesign{}
	for _, r := range d.Rules {
		c, e := loadGachaCharacterDesign(db, r.CostumeID)
		if e != nil {
			return d, e
		}
		d.Characters[r.CostumeID] = c
	}
	var defaults []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM SpecialScoutInfoTable WHERE id=0").Scan(&defaults); err != nil {
		return d, err
	}
	if err := decodeRecruitSpecialDefaults(defaults, &d); err != nil {
		return d, err
	}
	if len(d.Rules) == 0 {
		return d, fmt.Errorf("gamedata: empty recruitment table")
	}
	return d, nil
}

func decodeRecruitSpecialDefaults(defaults []byte, d *RecruitDesign) error {
	resetID, err := friendshipScalar(defaults, 5)
	if err != nil {
		return err
	}
	if resetID != 0 {
		return fmt.Errorf("gamedata: special recruitment currency has item ID")
	}
	for i, dst := range []*uint64{&d.AppearCount, &d.AutoResetMinute, &d.ResetCount, &d.ResetType, &d.ResetLimit} {
		v, e := friendshipScalar(defaults, []int{1, 2, 4, 6, 7}[i])
		if e != nil {
			return e
		}
		*dst = v
	}
	if d.AppearCount == 0 || d.AppearCount > math.MaxInt32 || d.AutoResetMinute == 0 || d.AutoResetMinute > uint64(math.MaxInt64/int64(time.Minute)) || d.ResetType != 3 || d.ResetCount == 0 || d.ResetCount > math.MaxInt32 || d.ResetLimit == 0 || d.ResetLimit > math.MaxInt32 {
		return fmt.Errorf("gamedata: invalid special recruitment defaults")
	}
	return nil
}
