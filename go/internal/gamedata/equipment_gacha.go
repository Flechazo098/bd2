package gamedata

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
)

// EquipmentGachaCatalog is the server-owned portion of exclusive-equipment
// draws. Scheduled groups and standalone ticket-only draws are both derived
// from GameData; the server does not enumerate product IDs in request logic.
type EquipmentGachaCatalog struct {
	Gachas    map[uint64]EquipmentGacha
	groups    map[uint64]EquipmentGachaGroup
	byGacha   map[uint64]uint64
	fixed     EquipmentFixedDesign
	equipment map[uint64]EquipmentDesign
}
type EquipmentGacha struct {
	ID               uint64
	Count            int
	PriceType, Price uint64
	TicketIDs        []uint64
	Pool             []WeightedEquipment
	TicketOnly       bool
}
type EquipmentGachaGroup struct{ ID, FixedID, PointCount, OneTimeGachaID, TenTimeGachaID uint64 }
type EquipmentFixedDesign struct {
	ID, SRCount, URCount uint64
	Reset                bool
}
type WeightedEquipment struct {
	ID, Weight uint64
	Children   []WeightedEquipment
}
type EquipmentDesign struct {
	ID          uint64
	Grade       uint64
	Main, Sub   []OptionGroup
	Private     []OptionGroup
	RankGroupID uint64
}
type OptionGroup struct {
	ID      uint64
	Choices []WeightedOption
}
type WeightedOption struct{ ID, Weight uint64 }
type EquipmentOptionChoice struct{ GroupID, ID uint64 }

func LoadEquipmentGacha(root, version string) (*EquipmentGachaCatalog, error) {
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-equipment-gacha-")
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
	c := &EquipmentGachaCatalog{Gachas: map[uint64]EquipmentGacha{}, groups: map[uint64]EquipmentGachaGroup{}, byGacha: map[uint64]uint64{}, equipment: map[uint64]EquipmentDesign{}}
	// Group 10002 is the permanent equipment draw actually submitted by the
	// 2.34.13 client as GachaTable 200/201. Groups 9/133/206 are current
	// pickup definitions retained for their own IDs.
	for _, groupID := range []uint64{10002, 9, 133, 206} {
		var raw []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM GachaGroupTable WHERE id=?", groupID).Scan(&raw); err != nil {
			return nil, err
		}
		one, _ := packedInts(raw, 24)
		ten, _ := packedInts(raw, 33)
		fixed, _ := packedInts(raw, 10)
		points, _ := packedInts(raw, 27)
		if len(one) != 1 || len(ten) != 1 || len(fixed) != 1 || fixed[0] != 1 || len(points) != 1 || points[0] != 1 {
			return nil, fmt.Errorf("gamedata: equipment group %d malformed", groupID)
		}
		// The absence of SelectCount/GachaSubType is deliberate evidence that
		// this live group is not an equipment 12PICK configuration.
		for _, field := range []int{16, 29, 31, 36} {
			if v, _ := packedInts(raw, field); len(v) != 0 {
				return nil, fmt.Errorf("gamedata: equipment group %d unexpectedly has selection field %d=%v", groupID, field, v)
			}
		}
		group := EquipmentGachaGroup{ID: groupID, FixedID: fixed[0], PointCount: points[0], OneTimeGachaID: one[0], TenTimeGachaID: ten[0]}
		c.groups[groupID] = group
		for _, id := range []uint64{one[0], ten[0]} {
			g, err := loadEquipmentGacha(db, id)
			if err != nil {
				return nil, err
			}
			c.Gachas[id] = g
			c.byGacha[id] = groupID
			for _, item := range g.Pool {
				if err := c.loadEquipmentTree(db, item); err != nil {
					return nil, err
				}
			}
		}
	}
	// Guaranteed equipment tickets are standalone GachaTable rows: they have
	// no diamond price, at least one resource-ticket id, and an equipment-only
	// RewardGroup tree. Discover every such row instead of special-casing a
	// product number from a log.
	rows, err := db.Query("SELECT id,ProtoBuf FROM GachaTable ORDER BY id")
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
		if _, exists := c.Gachas[id]; exists {
			continue
		}
		count, _ := packedInts(raw, 5)
		reward, _ := packedInts(raw, 7)
		price, _ := packedInts(raw, 10)
		kind, _ := packedInts(raw, 12)
		tickets, _ := packedInts(raw, 8)
		if len(count) != 1 || count[0] == 0 || len(reward) != 1 || len(tickets) == 0 || len(price) != 0 || len(kind) != 0 {
			continue
		}
		pool, equipmentOnly, err := classifyEquipmentRewardPool(db, reward[0])
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: ticket gacha %d: %w", id, err)
		}
		if !equipmentOnly {
			continue
		}
		g := EquipmentGacha{ID: id, Count: int(count[0]), TicketIDs: append([]uint64(nil), tickets...), Pool: pool, TicketOnly: true}
		c.Gachas[id] = g
		for _, item := range pool {
			if err := c.loadEquipmentTree(db, item); err != nil {
				rows.Close()
				return nil, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var fixed []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM GachaFixedTable WHERE id=1").Scan(&fixed); err != nil {
		return nil, err
	}
	sr, _ := packedInts(fixed, 1)
	ur, _ := packedInts(fixed, 3)
	reset, _ := packedInts(fixed, 6)
	if len(sr) != 1 || sr[0] != 10 || len(ur) != 1 || ur[0] != 100 || len(reset) != 1 || reset[0] != 1 {
		return nil, fmt.Errorf("gamedata: malformed equipment fixed table")
	}
	c.fixed = EquipmentFixedDesign{ID: 1, SRCount: sr[0], URCount: ur[0], Reset: true}
	return c, nil
}

func classifyEquipmentRewardPool(db *sql.DB, groupID uint64) ([]WeightedEquipment, bool, error) {
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM RewardGroupTable WHERE id=?", groupID).Scan(&raw); err != nil {
		return nil, false, err
	}
	ids, _ := packedInts(raw, 5)
	types, _ := packedInts(raw, 6)
	weights, _ := packedInts(raw, 8)
	if len(ids) == 0 || len(ids) != len(types) || len(ids) != len(weights) {
		return nil, false, errors.New("malformed reward group")
	}
	out := make([]WeightedEquipment, 0, len(ids))
	for i, id := range ids {
		entry := WeightedEquipment{Weight: weights[i]}
		if entry.Weight == 0 {
			return nil, false, errors.New("reward group has zero weight")
		}
		switch types[i] {
		case 10:
			entry.ID = id
		case 9:
			children, equipmentOnly, err := classifyEquipmentRewardPool(db, id)
			if err != nil {
				return nil, false, err
			}
			if !equipmentOnly {
				return nil, false, nil
			}
			entry.Children = children
		default:
			return nil, false, nil
		}
		out = append(out, entry)
	}
	return out, true, nil
}

func loadEquipmentGacha(db *sql.DB, id uint64) (EquipmentGacha, error) {
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM GachaTable WHERE id=?", id).Scan(&raw); err != nil {
		return EquipmentGacha{}, err
	}
	count, _ := packedInts(raw, 5)
	reward, _ := packedInts(raw, 7)
	price, _ := packedInts(raw, 10)
	kind, _ := packedInts(raw, 12)
	tickets, _ := packedInts(raw, 8)
	if len(count) != 1 || len(reward) != 1 || len(price) != 1 || len(kind) != 1 || kind[0] != 3 || price[0] != uint64(count[0])*200 {
		return EquipmentGacha{}, fmt.Errorf("gamedata: malformed equipment gacha %d", id)
	}
	pool, err := loadEquipmentRewardPool(db, reward[0])
	if err != nil {
		return EquipmentGacha{}, fmt.Errorf("gamedata: equipment gacha %d: %w", id, err)
	}
	var want []uint64
	switch len(pool) {
	case 8: // permanent draw 200/201: one 3% five-star UR branch.
		want = []uint64{300, 200, 250, 850, 1700, 400, 1600, 4700}
	case 9: // pickup groups: UP and non-UP five-star UR branches.
		want = []uint64{150, 150, 200, 250, 850, 1700, 400, 1600, 4700}
	default:
		return EquipmentGacha{}, fmt.Errorf("gamedata: equipment gacha %d has %d branches", id, len(pool))
	}
	for i := range pool {
		if pool[i].Weight != want[i] {
			return EquipmentGacha{}, fmt.Errorf("gamedata: equipment gacha %d branch %d rate=%d", id, i, pool[i].Weight)
		}
	}
	return EquipmentGacha{ID: id, Count: int(count[0]), PriceType: kind[0], Price: price[0], TicketIDs: append([]uint64(nil), tickets...), Pool: pool}, nil
}
func loadEquipmentRewardPool(db *sql.DB, groupID uint64) ([]WeightedEquipment, error) {
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM RewardGroupTable WHERE id=?", groupID).Scan(&raw); err != nil {
		return nil, err
	}
	ids, _ := packedInts(raw, 5)
	types, _ := packedInts(raw, 6)
	weights, _ := packedInts(raw, 8)
	if len(ids) == 0 || len(ids) != len(types) || len(ids) != len(weights) {
		return nil, errors.New("malformed equipment reward group")
	}
	out := make([]WeightedEquipment, 0, len(ids))
	for i, id := range ids {
		v := WeightedEquipment{Weight: weights[i]}
		switch types[i] {
		case 10:
			v.ID = id
		case 9:
			children, err := loadEquipmentRewardPool(db, id)
			if err != nil {
				return nil, err
			}
			v.Children = children
		default:
			return nil, fmt.Errorf("unsupported equipment reward type %d", types[i])
		}
		out = append(out, v)
	}
	return out, nil
}
func (c *EquipmentGachaCatalog) loadEquipmentTree(db *sql.DB, item WeightedEquipment) error {
	if item.ID == 0 {
		for _, child := range item.Children {
			if err := c.loadEquipmentTree(db, child); err != nil {
				return err
			}
		}
		return nil
	}
	if _, ok := c.equipment[item.ID]; ok {
		return nil
	}
	d, err := loadEquipmentDesign(db, item.ID)
	if err != nil {
		return err
	}
	c.equipment[item.ID] = d
	return nil
}
func loadEquipmentDesign(db *sql.DB, id uint64) (EquipmentDesign, error) {
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM EquipmentTable WHERE id=?", id).Scan(&raw); err != nil {
		return EquipmentDesign{}, err
	}
	main, _ := packedInts(raw, 12)
	private, _ := packedInts(raw, 17)
	sub, _ := packedInts(raw, 21)
	rank, _ := packedInts(raw, 19)
	grade, _ := packedInts(raw, 3)
	if len(grade) != 1 || grade[0] == 0 {
		return EquipmentDesign{}, fmt.Errorf("gamedata: equipment %d malformed grade", id)
	}
	d := EquipmentDesign{ID: id, Grade: grade[0]}
	var err error
	if d.Main, err = loadOptionGroups(db, main); err != nil {
		return d, err
	}
	if d.Sub, err = loadOptionGroups(db, sub); err != nil {
		return d, err
	}
	if d.Private, err = loadOptionGroups(db, private); err != nil {
		return d, err
	}
	if len(rank) == 1 {
		d.RankGroupID = rank[0]
	} else if len(rank) != 0 {
		return d, fmt.Errorf("gamedata: equipment %d malformed rank group", id)
	}
	return d, nil
}
func loadOptionGroups(db *sql.DB, ids []uint64) ([]OptionGroup, error) {
	out := make([]OptionGroup, 0, len(ids))
	for _, groupID := range ids {
		rows, err := db.Query("SELECT ProtoBuf FROM EquipmentOptionTable WHERE GroupId=? ORDER BY id", groupID)
		if err != nil {
			return nil, err
		}
		g := OptionGroup{ID: groupID}
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return nil, err
			}
			id, _ := packedInts(raw, 5)
			weight, _ := packedInts(raw, 2)
			if len(id) != 1 || len(weight) != 1 || weight[0] == 0 {
				rows.Close()
				return nil, fmt.Errorf("gamedata: malformed equipment option group %d", groupID)
			}
			g.Choices = append(g.Choices, WeightedOption{ID: id[0], Weight: weight[0]})
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if len(g.Choices) == 0 {
			return nil, fmt.Errorf("gamedata: equipment option group %d empty", groupID)
		}
		out = append(out, g)
	}
	return out, nil
}
func (c *EquipmentGachaCatalog) Gacha(id uint64) (EquipmentGacha, bool) {
	g, ok := c.Gachas[id]
	return g, ok
}
func (c *EquipmentGachaCatalog) GroupForGacha(id uint64) (EquipmentGachaGroup, bool) {
	group, ok := c.groups[c.byGacha[id]]
	return group, ok
}
func (c *EquipmentGachaCatalog) Fixed() EquipmentFixedDesign { return c.fixed }
func (c *EquipmentGachaCatalog) RollOptions(id uint64) (main, sub []EquipmentOptionChoice, private *EquipmentOptionChoice, err error) {
	d, ok := c.equipment[id]
	if !ok {
		return nil, nil, nil, fmt.Errorf("gamedata: equipment %d absent from active gacha", id)
	}
	pick := func(groups []OptionGroup) ([]EquipmentOptionChoice, error) {
		out := make([]EquipmentOptionChoice, 0, len(groups))
		for _, g := range groups {
			options := make([]WeightedEquipment, 0, len(g.Choices))
			for _, choice := range g.Choices {
				options = append(options, WeightedEquipment{ID: choice.ID, Weight: choice.Weight})
			}
			picked, e := rollEquipmentChoiceWith(options, cryptoDraw)
			if e != nil {
				return nil, e
			}
			out = append(out, EquipmentOptionChoice{GroupID: g.ID, ID: picked})
		}
		return out, nil
	}
	if main, err = pick(d.Main); err != nil {
		return
	}
	if sub, err = pick(d.Sub); err != nil {
		return
	}
	choices, err := pick(d.Private)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(choices) > 1 {
		return nil, nil, nil, fmt.Errorf("gamedata: equipment %d has %d private option groups", id, len(choices))
	}
	if len(choices) == 1 {
		private = &choices[0]
	}
	return
}
func (g EquipmentGacha) Roll(previousSR, previousUR uint64, fixed EquipmentFixedDesign) ([]uint64, EquipmentRoll, error) {
	if g.TicketOnly {
		state := EquipmentRoll{SRCount: previousSR, URCount: previousUR, SRSort: -1, URSort: -1}
		if g.Count <= 0 || len(g.Pool) == 0 {
			return nil, state, errors.New("gamedata: invalid ticket equipment gacha")
		}
		out := make([]uint64, g.Count)
		for i := range out {
			id, err := rollEquipmentChoiceWith(g.Pool, cryptoDraw)
			if err != nil {
				return nil, state, err
			}
			out[i] = id
		}
		return out, state, nil
	}
	return g.rollWith(previousSR, previousUR, fixed, cryptoDraw)
}

type EquipmentRoll struct {
	SRCount, URCount uint64
	SRSort, URSort   int
}

func (g EquipmentGacha) rollWith(sr, ur uint64, fixed EquipmentFixedDesign, draw func(uint64) (uint64, error)) ([]uint64, EquipmentRoll, error) {
	state := EquipmentRoll{SRCount: sr, URCount: ur, SRSort: -1, URSort: -1}
	if g.Count <= 0 || (len(g.Pool) != 8 && len(g.Pool) != 9) {
		return nil, state, errors.New("gamedata: invalid equipment gacha")
	}
	out := make([]uint64, g.Count)
	for i := range out {
		forceUR := state.URCount+1 >= fixed.URCount
		forceSR := !forceUR && state.SRCount+1 >= fixed.SRCount
		var id uint64
		var err error
		switch {
		case forceUR:
			id, err = rollEquipmentChoiceWith(equipmentGuaranteedPool(g.Pool, true), draw)
			state.URSort = i
		case forceSR:
			id, err = rollEquipmentChoiceWith(equipmentGuaranteedPool(g.Pool, false), draw)
			state.SRSort = i
		default:
			id, err = rollEquipmentChoiceWith(g.Pool, draw)
		}
		if err != nil {
			return nil, state, err
		}
		out[i] = id
		tier := equipmentTier(g.Pool, id)
		switch tier {
		case 2:
			state.SRCount = 0
			state.URCount = 0
		case 1:
			state.SRCount = 0
			state.URCount++
		default:
			state.SRCount++
			state.URCount++
		}
	}
	return out, state, nil
}

func equipmentGuaranteedPool(pool []WeightedEquipment, ur bool) []WeightedEquipment {
	if len(pool) == 8 {
		if ur {
			return []WeightedEquipment{{Weight: 15, Children: pool[0:1]}, {Weight: 35, Children: pool[2:3]}, {Weight: 50, Children: pool[5:6]}}
		}
		return []WeightedEquipment{{Weight: 15, Children: pool[1:2]}, {Weight: 35, Children: pool[3:4]}, {Weight: 50, Children: pool[6:7]}}
	}
	if ur {
		return []WeightedEquipment{{Weight: 15, Children: pool[0:2]}, {Weight: 35, Children: pool[3:4]}, {Weight: 50, Children: pool[6:7]}}
	}
	return []WeightedEquipment{{Weight: 15, Children: pool[2:3]}, {Weight: 35, Children: pool[4:5]}, {Weight: 50, Children: pool[7:8]}}
}
func equipmentTier(pool []WeightedEquipment, id uint64) int {
	for i, item := range pool {
		if containsEquipment(item, id) {
			if len(pool) == 8 {
				switch i {
				case 0, 2, 5:
					return 2
				case 1, 3, 6:
					return 1
				default:
					return 0
				}
			}
			switch i {
			case 0, 1, 3, 6:
				return 2
			case 2, 4, 7:
				return 1
			default:
				return 0
			}
		}
	}
	return -1
}
func containsEquipment(item WeightedEquipment, id uint64) bool {
	if item.ID != 0 {
		return item.ID == id
	}
	for _, child := range item.Children {
		if containsEquipment(child, id) {
			return true
		}
	}
	return false
}
func rollEquipmentChoiceWith(pool []WeightedEquipment, draw func(uint64) (uint64, error)) (uint64, error) {
	var total uint64
	for _, item := range pool {
		if item.Weight == 0 || math.MaxUint64-total < item.Weight {
			return 0, errors.New("gamedata: invalid equipment weight")
		}
		total += item.Weight
	}
	value, err := draw(total)
	if err != nil {
		return 0, err
	}
	if value >= total {
		return 0, errors.New("gamedata: random source out of range")
	}
	for _, item := range pool {
		if value < item.Weight {
			if item.ID != 0 {
				return item.ID, nil
			}
			return rollEquipmentChoiceWith(item.Children, draw)
		}
		value -= item.Weight
	}
	return 0, errors.New("gamedata: equipment selection failed")
}
func cryptoDraw(limit uint64) (uint64, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).SetUint64(limit))
	if err != nil {
		return 0, err
	}
	return n.Uint64(), nil
}
