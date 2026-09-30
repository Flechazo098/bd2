package gamedata

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
)

// FirstGachaDesign is the free starter reroll referenced by GameDefaultTable.
// It is separate from ordinary and cash-product gachas because its reward
// program emits both costumes and equipment and has no purchase currency.
type FirstGachaDesign struct {
	Group       GachaGroupDesign
	GachaID     uint64
	Count       int
	RewardGroup *FirstGachaRewardGroup
	characters  map[uint64]CharacterDesign
	equipment   *EquipmentGachaCatalog
}

type FirstGachaReward struct{ Type, ID uint64 }
type FirstGachaRewardGroup struct {
	ID, DropCount, DropType uint64
	Entries                 []FirstGachaRewardEntry
}
type FirstGachaRewardEntry struct {
	ItemType, ItemID, Count, Weight uint64
	Group                           *FirstGachaRewardGroup
}

func (d *FirstGachaDesign) Character(id uint64) (CharacterDesign, bool) {
	v, ok := d.characters[id]
	return v, ok
}
func (d *FirstGachaDesign) EquipmentCatalog() *EquipmentGachaCatalog { return d.equipment }

// CostumeCatalog supplies character and duplicate-upgrade metadata only. It
// deliberately has no purchasable products, so it cannot enable a zero-price
// ordinary draw through the normal catalog.
func (d *FirstGachaDesign) CostumeCatalog() *RegularGachaCatalog {
	return &RegularGachaCatalog{characters: d.characters}
}

func NewFirstGachaDesign(group GachaGroupDesign, id uint64, count int, program *FirstGachaRewardGroup, characters map[uint64]CharacterDesign, equipment map[uint64]EquipmentDesign) (*FirstGachaDesign, error) {
	if group.ID == 0 || group.GachaSubType != 3 || group.GachaType != 0 || group.TenTimeGachaID != id || id == 0 || count <= 0 || count > 100 {
		return nil, errors.New("gamedata: invalid first gacha identity")
	}
	n, err := firstGachaRewardCount(program, map[*FirstGachaRewardGroup]bool{})
	if err != nil || n != uint64(count) {
		return nil, fmt.Errorf("gamedata: first gacha reward count=%d want=%d: %v", n, count, err)
	}
	d := &FirstGachaDesign{Group: group, GachaID: id, Count: count, RewardGroup: program, characters: map[uint64]CharacterDesign{}, equipment: &EquipmentGachaCatalog{equipment: map[uint64]EquipmentDesign{}}}
	var visit func(*FirstGachaRewardGroup) error
	visit = func(g *FirstGachaRewardGroup) error {
		for _, e := range g.Entries {
			switch e.ItemType {
			case 9:
				if err := visit(e.Group); err != nil {
					return err
				}
			case 11:
				c, ok := characters[e.ItemID]
				if !ok || c.ID == 0 || c.HP == 0 {
					return fmt.Errorf("gamedata: first gacha costume %d has no character", e.ItemID)
				}
				d.characters[e.ItemID] = c
			case 10:
				v, ok := equipment[e.ItemID]
				if !ok || v.ID != e.ItemID || v.Grade == 0 {
					return fmt.Errorf("gamedata: first gacha equipment %d has no design", e.ItemID)
				}
				d.equipment.equipment[e.ItemID] = v
			}
		}
		return nil
	}
	if err := visit(program); err != nil {
		return nil, err
	}
	return d, nil
}

func LoadFirstGacha(root, version string) (*FirstGachaDesign, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	return loadFirstGacha(db)
}

func loadFirstGacha(db *sql.DB) (*FirstGachaDesign, error) {
	read := func(table string, id uint64) ([]byte, error) {
		var b []byte
		err := db.QueryRow("SELECT ProtoBuf FROM "+table+" WHERE id=?", id).Scan(&b)
		return b, err
	}
	defaults, err := read("GameDefaultTable", 0)
	if err != nil {
		return nil, err
	}
	first, err := packedInts(defaults, 46)
	if err != nil || len(first) != 1 || first[0] == 0 {
		return nil, errors.New("gamedata: missing FirstLimitGachaId")
	}
	raw, err := read("GachaGroupTable", first[0])
	if err != nil {
		return nil, err
	}
	value := func(b []byte, n int) (uint64, error) {
		v, err := packedInts(b, n)
		if err != nil || len(v) > 1 {
			return 0, fmt.Errorf("gamedata: malformed first gacha field %d", n)
		}
		if len(v) == 0 {
			return 0, nil
		}
		return v[0], nil
	}
	id, err := value(raw, 18)
	if err != nil || id != first[0] {
		return nil, errors.New("gamedata: first gacha group identity mismatch")
	}
	for field, want := range map[int]uint64{16: 3, 17: 0, 28: 0, 3: 0, 4: 0, 5: 0, 24: 0} {
		v, e := value(raw, field)
		if e != nil || v != want {
			return nil, fmt.Errorf("gamedata: first gacha group %d has unsupported field %d=%d", id, field, v)
		}
	}
	gachaID, err := value(raw, 33)
	if err != nil || gachaID == 0 {
		return nil, errors.New("gamedata: first gacha product missing")
	}
	product, err := read("GachaTable", gachaID)
	if err != nil {
		return nil, err
	}
	for field, want := range map[int]uint64{9: gachaID, 1: 0, 2: 0, 3: 0, 4: 0, 10: 0, 11: 0, 12: 0} {
		v, e := value(product, field)
		if e != nil || v != want {
			return nil, fmt.Errorf("gamedata: first gacha product has unsupported field %d=%d", field, v)
		}
	}
	tickets, err := packedInts(product, 8)
	if err != nil {
		return nil, err
	}
	for _, id := range tickets {
		if id != 0 {
			return nil, errors.New("gamedata: first gacha cannot consume tickets")
		}
	}
	count, err := value(product, 5)
	if err != nil || count == 0 || count > 100 {
		return nil, errors.New("gamedata: first gacha count invalid")
	}
	rewardID, err := value(product, 7)
	if err != nil || rewardID == 0 {
		return nil, errors.New("gamedata: first gacha reward missing")
	}
	program, err := loadFirstGachaRewardGroup(db, rewardID, map[uint64]bool{})
	if err != nil {
		return nil, err
	}
	characters := map[uint64]CharacterDesign{}
	equipment := map[uint64]EquipmentDesign{}
	var load func(*FirstGachaRewardGroup) error
	load = func(g *FirstGachaRewardGroup) error {
		for _, e := range g.Entries {
			switch e.ItemType {
			case 9:
				if err := load(e.Group); err != nil {
					return err
				}
			case 11:
				if _, ok := characters[e.ItemID]; !ok {
					v, err := loadGachaCharacterDesign(db, e.ItemID)
					if err != nil {
						return err
					}
					characters[e.ItemID] = v
				}
			case 10:
				if _, ok := equipment[e.ItemID]; !ok {
					v, err := loadEquipmentDesign(db, e.ItemID)
					if err != nil {
						return err
					}
					equipment[e.ItemID] = v
				}
			}
		}
		return nil
	}
	if err := load(program); err != nil {
		return nil, err
	}
	points, err := value(raw, 27)
	if err != nil {
		return nil, err
	}
	group := GachaGroupDesign{ID: id, GachaSubType: 3, TenTimeGachaID: gachaID, PointCount: points}
	return NewFirstGachaDesign(group, gachaID, int(count), program, characters, equipment)
}

func loadFirstGachaRewardGroup(db *sql.DB, id uint64, visiting map[uint64]bool) (*FirstGachaRewardGroup, error) {
	if visiting[id] || len(visiting) > 32 {
		return nil, fmt.Errorf("gamedata: first gacha reward cycle/depth at %d", id)
	}
	visiting[id] = true
	defer delete(visiting, id)
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM RewardGroupTable WHERE id=?", id).Scan(&raw); err != nil {
		return nil, err
	}
	fields := map[int][]uint64{}
	for _, n := range []int{1, 2, 4, 5, 6, 8} {
		v, err := packedInts(raw, n)
		if err != nil {
			return nil, err
		}
		fields[n] = v
	}
	if len(fields[1]) != 1 || len(fields[2]) > 1 {
		return nil, errors.New("gamedata: malformed first reward execution")
	}
	g := &FirstGachaRewardGroup{ID: id, DropCount: fields[1][0]}
	if len(fields[2]) == 1 {
		g.DropType = fields[2][0]
	}
	ids := fields[5]
	if len(ids) == 0 || len(ids) != len(fields[4]) || len(ids) != len(fields[6]) || len(ids) != len(fields[8]) {
		return nil, errors.New("gamedata: malformed first reward entries")
	}
	for i, id := range ids {
		e := FirstGachaRewardEntry{ItemID: id, ItemType: fields[6][i], Count: fields[4][i], Weight: fields[8][i]}
		if e.ItemType == 9 {
			child, err := loadFirstGachaRewardGroup(db, id, visiting)
			if err != nil {
				return nil, err
			}
			e.Group = child
		}
		g.Entries = append(g.Entries, e)
	}
	if _, err := firstGachaRewardCount(g, map[*FirstGachaRewardGroup]bool{}); err != nil {
		return nil, err
	}
	return g, nil
}

func firstGachaRewardCount(g *FirstGachaRewardGroup, visiting map[*FirstGachaRewardGroup]bool) (uint64, error) {
	if g == nil || g.ID == 0 || g.DropCount == 0 || g.DropCount > 100 || len(g.Entries) == 0 || g.DropType > 1 || visiting[g] || len(visiting) > 32 {
		return 0, errors.New("gamedata: invalid first reward program")
	}
	visiting[g] = true
	defer delete(visiting, g)
	var total, each, weight uint64
	for _, e := range g.Entries {
		if e.ItemID == 0 || e.Count == 0 || e.Count > 100 || e.Weight == 0 || math.MaxUint64-weight < e.Weight {
			return 0, errors.New("gamedata: invalid first reward entry")
		}
		weight += e.Weight
		n := e.Count
		switch e.ItemType {
		case 9:
			if e.Group == nil || e.Group.ID != e.ItemID {
				return 0, errors.New("gamedata: first reward child mismatch")
			}
			child, err := firstGachaRewardCount(e.Group, visiting)
			if err != nil || child > 100/e.Count {
				return 0, errors.New("gamedata: invalid first reward child count")
			}
			n = child * e.Count
		case 10, 11:
			if e.Group != nil {
				return 0, errors.New("gamedata: direct first reward has a child")
			}
		default:
			return 0, fmt.Errorf("gamedata: unsupported first reward type %d", e.ItemType)
		}
		if g.DropType == 0 {
			if each == 0 {
				each = n
			} else if each != n {
				return 0, errors.New("gamedata: weighted first reward cardinality differs")
			}
		} else {
			total += n
		}
	}
	if g.DropType == 0 {
		total = each * g.DropCount
	}
	if total > 100 {
		return 0, errors.New("gamedata: first reward count exceeds bound")
	}
	return total, nil
}

func (d *FirstGachaDesign) Roll() ([]FirstGachaReward, error) { return d.roll(cryptoDraw) }
func (d *FirstGachaDesign) roll(draw func(uint64) (uint64, error)) ([]FirstGachaReward, error) {
	if d == nil {
		return nil, errors.New("gamedata: missing first gacha")
	}
	if n, err := firstGachaRewardCount(d.RewardGroup, map[*FirstGachaRewardGroup]bool{}); err != nil || n != uint64(d.Count) {
		return nil, errors.New("gamedata: first reward program changed")
	}
	var out []FirstGachaReward
	var run func(*FirstGachaRewardGroup) error
	var emit func(FirstGachaRewardEntry) error
	emit = func(e FirstGachaRewardEntry) error {
		for n := uint64(0); n < e.Count; n++ {
			if e.ItemType == 9 {
				if err := run(e.Group); err != nil {
					return err
				}
			} else {
				out = append(out, FirstGachaReward{Type: e.ItemType, ID: e.ItemID})
			}
		}
		return nil
	}
	run = func(g *FirstGachaRewardGroup) error {
		if g.DropType == 1 {
			for _, e := range g.Entries {
				if err := emit(e); err != nil {
					return err
				}
			}
			return nil
		}
		var sum uint64
		for _, e := range g.Entries {
			sum += e.Weight
		}
		for n := uint64(0); n < g.DropCount; n++ {
			v, err := draw(sum)
			if err != nil {
				return err
			}
			if v >= sum {
				return errors.New("gamedata: first reward random out of bounds")
			}
			for _, e := range g.Entries {
				if v < e.Weight {
					if err := emit(e); err != nil {
						return err
					}
					break
				}
				v -= e.Weight
			}
		}
		return nil
	}
	if err := run(d.RewardGroup); err != nil {
		return nil, err
	}
	if len(out) != d.Count {
		return nil, errors.New("gamedata: first reward count mismatch")
	}
	return out, nil
}
