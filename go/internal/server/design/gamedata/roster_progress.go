package gamedata

import (
	"fmt"
	"math"
)

type PartnerRule struct {
	Weight   float64
	Partners []uint64
	Rewards  []Reward
	Stories  []uint64
}
type SolidarityRule struct {
	Require uint64
	Rewards []Reward
}
type CostumeProgressRule struct{ Group, Maximum, Unique, NotTrash uint64 }
type RosterProgressDesign struct {
	Power                 [10]uint64
	EquipmentGroups       map[uint64][2]uint64
	EquipmentGrowthPoints map[[2]uint64]uint64
	EquipmentRankPoints   map[[2]uint64][]uint64

	Partners                            map[uint64]PartnerRule
	Solidarity                          map[uint64]SolidarityRule
	PartnerMaximum, WinPoint, LosePoint uint64
	Costumes                            map[uint64]CostumeProgressRule
	Growth                              map[[2]uint64][]Reward
	AllRounder                          map[[2]uint64]CashProductDesign
}

func LoadRosterProgressDesign(root, version string) (*RosterProgressDesign, error) {
	db, done, err := OpenDatabase(root, version, "common")
	if err != nil {
		return nil, err
	}
	defer done()
	d := &RosterProgressDesign{Partners: map[uint64]PartnerRule{}, Solidarity: map[uint64]SolidarityRule{}, Costumes: map[uint64]CostumeProgressRule{}, Growth: map[[2]uint64][]Reward{}, AllRounder: map[[2]uint64]CashProductDesign{}}
	var raw []byte
	if err = db.QueryRow("SELECT ProtoBuf FROM GameDefaultTable WHERE id=0").Scan(&raw); err != nil {
		return nil, err
	}
	d.PartnerMaximum, err = optionalScalar(raw, 87)
	if err != nil || d.PartnerMaximum == 0 {
		return nil, fmt.Errorf("gamedata: invalid partner maximum")
	}
	d.WinPoint, _ = optionalScalar(raw, 123)
	d.LosePoint, _ = optionalScalar(raw, 65)
	read := func(table string, visit func([]byte) error) error { return readCashMetadata(db, table, visit) }
	rewards := func(raw []byte, count, id, typ int) ([]Reward, error) {
		a, e := packedInts(raw, count)
		if e != nil {
			return nil, e
		}
		b, e := packedInts(raw, id)
		if e != nil {
			return nil, e
		}
		c, e := packedInts(raw, typ)
		if e != nil || len(a) != len(b) || len(a) != len(c) {
			return nil, fmt.Errorf("gamedata: inconsistent roster rewards")
		}
		out := make([]Reward, len(a))
		for i := range a {
			out[i] = Reward{Type: c[i], ID: b[i], Count: a[i]}
		}
		return out, nil
	}
	if err = read("LikabilityPartnerTable", func(raw []byte) error {
		id, _ := optionalScalar(raw, 1)
		w, _, e := fixed64Double(raw, 2)
		if e != nil || math.IsNaN(w) || math.IsInf(w, 0) {
			return fmt.Errorf("gamedata: invalid partner weight")
		}
		p, e := packedInts(raw, 3)
		if e != nil {
			return e
		}
		r, e := rewards(raw, 4, 5, 6)
		if e != nil {
			return e
		}
		st, e := packedInts(raw, 7)
		if e != nil || len(p) != len(r) || len(p) != len(st) {
			return fmt.Errorf("gamedata: inconsistent partner rules")
		}
		d.Partners[id] = PartnerRule{w, p, r, st}
		return nil
	}); err != nil {
		return nil, err
	}
	if err = read("SolidarityLevelTable", func(raw []byte) error {
		id, _ := optionalScalar(raw, 1)
		n, _ := optionalScalar(raw, 5)
		r, e := rewards(raw, 2, 3, 4)
		d.Solidarity[id] = SolidarityRule{n, r}
		return e
	}); err != nil {
		return nil, err
	}
	if err = read("CostumeTable", func(raw []byte) error {
		v, e := cashScalars(raw, 11, 10, 15, 27, 21)
		if e != nil {
			return e
		}
		d.Costumes[v[0]] = CostumeProgressRule{v[1], v[2], v[3], v[4]}
		return nil
	}); err != nil {
		return nil, err
	}
	if err = read("CostumeGrowthTable", func(raw []byte) error {
		g, _ := optionalScalar(raw, 4)
		id, _ := optionalScalar(raw, 8)
		r, e := rewards(raw, 5, 6, 7)
		d.Growth[[2]uint64{g, id}] = r
		return e
	}); err != nil {
		return nil, err
	}
	resolver, err := LoadCashRewardResolver(root, version)
	if err != nil {
		return nil, err
	}
	catalog, err := loadCashCatalog(db)
	if err != nil {
		return nil, err
	}
	for _, p := range catalog.Products {
		if p.RandomBoxID == 0 {
			continue
		}
		group := resolver.groups[resolver.boxes[p.RandomBoxID]]
		types, e := packedInts(group, 6)
		if e != nil {
			return nil, e
		}
		ids, e := packedInts(group, 5)
		if e != nil {
			return nil, e
		}
		counts, e := packedInts(group, 4)
		if e != nil {
			return nil, e
		}
		if len(types) != 1 || len(ids) != 1 || len(counts) != 1 || types[0] != 29 || counts[0] != 1 {
			continue
		}

		var instant []byte
		if e = db.QueryRow("SELECT ProtoBuf FROM InstantUseItemTable WHERE id=?", ids[0]).Scan(&instant); e != nil {
			return nil, e
		}
		typ, e := optionalScalar(instant, 6)
		if e != nil {
			return nil, e
		}
		if typ != 0 {
			continue
		}
		key := [2]uint64{p.Key.GroupID, p.Key.ProductID}
		if _, exists := d.AllRounder[key]; exists {
			return nil, fmt.Errorf("gamedata: ambiguous all-rounder product")
		}
		d.AllRounder[key] = p
	}
	d.EquipmentGroups = map[uint64][2]uint64{}
	d.EquipmentGrowthPoints = map[[2]uint64]uint64{}
	d.EquipmentRankPoints = map[[2]uint64][]uint64{}
	powerRows := 0
	if err = read("BattlePowerTable", func(raw []byte) error {
		powerRows++
		for _, n := range []int{1, 2, 3, 5, 6, 7, 8, 9} {
			v, e := optionalScalar(raw, n)
			if e != nil {
				return e
			}
			d.Power[n] = v
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if powerRows != 1 {
		return nil, fmt.Errorf("gamedata: ambiguous battle power rules")
	}
	if err = read("EquipmentTable", func(raw []byte) error {
		id, e := optionalScalar(raw, 6)
		if e != nil {
			return e
		}
		g, e := optionalScalar(raw, 4)
		if e != nil {
			return e
		}
		r, e := optionalScalar(raw, 19)
		if e != nil {
			return e
		}
		d.EquipmentGroups[id] = [2]uint64{g, r}
		return nil
	}); err != nil {
		return nil, err
	}
	if err = read("EquipmentGrowthTable", func(raw []byte) error {
		g, e := optionalScalar(raw, 4)
		if e != nil {
			return e
		}
		id, e := optionalScalar(raw, 6)
		if e != nil {
			return e
		}
		v, e := optionalScalar(raw, 5)
		d.EquipmentGrowthPoints[[2]uint64{g, id}] = v
		return e
	}); err != nil {
		return nil, err
	}
	if err = read("EquipmentRankTable", func(raw []byte) error {
		g, e := optionalScalar(raw, 1)
		if e != nil {
			return e
		}
		id, e := optionalScalar(raw, 3)
		if e != nil {
			return e
		}
		v, e := packedInts(raw, 2)
		d.EquipmentRankPoints[[2]uint64{g, id}] = v
		return e
	}); err != nil {
		return nil, err
	}
	return d, nil
}
