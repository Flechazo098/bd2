package gamedata

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"sort"

	_ "modernc.org/sqlite"

	"bd2server/internal/wire"
)

const (
	InfiniteGachaID        = 9100033
	InfiniteProductGroupID = 1100001
	InfiniteProductID      = 9100033
	officialRateScale      = 10000
	officialFiveStarRate   = 300
	officialFourStarRate   = 1400
)

// InfiniteGachaDesign is the non-account portion of the 2.34.13 paid
// "infinite reroll" product. The local server deliberately makes final
// confirmation free, but still reads the official result pool from GameData.
type InfiniteGachaDesign struct {
	Count        int
	CostumeIDs   []uint64 // five-star compatibility view
	FiveStarIDs  []uint64
	FourStarIDs  []uint64
	ThreeStarIDs []uint64
	characters   map[uint64]CharacterDesign
}

type CharacterDesign struct {
	ID                uint64
	HP                uint64
	CostumeMaxLevel   uint64
	OverflowItemType  uint64
	OverflowItemID    uint64
	OverflowItemCount uint64
}

type WeightedCostume struct {
	ID       uint64
	Weight   uint64
	Children []WeightedCostume
}

type RegularGacha struct {
	ID             uint64
	Count          int
	PriceType      uint64
	Price          uint64
	Pool           []WeightedCostume
	FixedCostumeID uint64
	TicketIDs      []uint64
}

type GachaGroupDesign struct {
	ID                         uint64
	FixedID                    uint64
	PointCount                 uint64
	PickUpExchangeCost         uint64
	PickUpCostumeID            uint64
	OneTimeGachaID             uint64
	TenTimeGachaID             uint64
	SelectCount                uint64
	SelectionChoiceRate        uint64
	UseSelectionOnlyFixedApply bool
	GachaSubType               uint64
	IsSelectedFromPity         bool
}

type GachaFixedDesign struct {
	ID                   uint64
	CostumeGrade4Count   uint64
	CostumeGrade5Count   uint64
	ResetOnMatchingGrade bool
}

type GachaFixedRoll struct {
	CostumeGrade4Count uint64
	CostumeGrade5Count uint64
	CostumeGrade4Sort  int
	CostumeGrade5Sort  int
	SelectionSorts     []int
}

type RegularGachaCatalog struct {
	Gachas     map[uint64]RegularGacha
	characters map[uint64]CharacterDesign
	groups     map[uint64]GachaGroupDesign
	byGacha    map[uint64]uint64
	fixed      map[uint64]GachaFixedDesign
	grades     map[uint64]uint64
}

// GachaMigrationFact is the minimal stable GameData view consumed by the
// cross-language state repair tool. It deliberately excludes roll weights and
// other online-only catalog fields.
type GachaMigrationFact struct {
	GachaID, GroupID, FixedID, PointCount uint64
}

type CostumeMigrationFact struct {
	CostumeID, Grade, MaxLevel                      uint64
	OverflowItemType, OverflowItemID, OverflowCount uint64
}

func (c *RegularGachaCatalog) MigrationFacts() ([]GachaMigrationFact, []CostumeMigrationFact) {
	if c == nil {
		return nil, nil
	}
	gachas := make([]GachaMigrationFact, 0, len(c.byGacha))
	for gachaID, groupID := range c.byGacha {
		group := c.groups[groupID]
		gachas = append(gachas, GachaMigrationFact{GachaID: gachaID, GroupID: group.ID, FixedID: group.FixedID, PointCount: group.PointCount})
	}
	sort.Slice(gachas, func(i, j int) bool { return gachas[i].GachaID < gachas[j].GachaID })
	costumes := make([]CostumeMigrationFact, 0, len(c.characters))
	for costumeID, design := range c.characters {
		costumes = append(costumes, CostumeMigrationFact{CostumeID: costumeID, Grade: c.grades[costumeID], MaxLevel: design.CostumeMaxLevel,
			OverflowItemType: design.OverflowItemType, OverflowItemID: design.OverflowItemID, OverflowCount: design.OverflowItemCount})
	}
	sort.Slice(costumes, func(i, j int) bool { return costumes[i].CostumeID < costumes[j].CostumeID })
	return gachas, costumes
}

func NewRegularGachaCatalog(gachas map[uint64]RegularGacha, characters map[uint64]CharacterDesign) (*RegularGachaCatalog, error) {
	if len(gachas) == 0 || len(characters) == 0 {
		return nil, errors.New("gamedata: invalid regular gacha catalog")
	}
	catalog := &RegularGachaCatalog{
		Gachas: make(map[uint64]RegularGacha, len(gachas)), characters: make(map[uint64]CharacterDesign, len(characters)),
		groups: map[uint64]GachaGroupDesign{}, byGacha: map[uint64]uint64{}, fixed: map[uint64]GachaFixedDesign{}, grades: map[uint64]uint64{},
	}
	for id, character := range characters {
		catalog.characters[id] = character
	}
	for id, gacha := range gachas {
		if id == 0 || gacha.ID != id || gacha.Count <= 0 || (gacha.PriceType != 2 && gacha.PriceType != 3) || gacha.Price == 0 {
			return nil, errors.New("gamedata: invalid regular gacha definition")
		}
		if err := validateCostumePool(gacha.Pool, catalog.characters); err != nil {
			return nil, err
		}
		if gacha.FixedCostumeID != 0 {
			if _, ok := catalog.characters[gacha.FixedCostumeID]; !ok {
				return nil, fmt.Errorf("gamedata: fixed costume %d lacks character design", gacha.FixedCostumeID)
			}
		}
		catalog.Gachas[id] = gacha
		catalog.recordPoolGrades(gacha)
	}
	return catalog, nil
}

// AddGroupDesign supports small, schema-accurate fixtures. Production uses
// loadGroupsAndFixed to read the same fields from the live GameData tables.
func (c *RegularGachaCatalog) AddGroupDesign(group GachaGroupDesign, fixed GachaFixedDesign) error {
	if c == nil || group.ID == 0 || group.PointCount == 0 {
		return errors.New("gamedata: invalid gacha group")
	}
	if group.OneTimeGachaID != 0 {
		if _, ok := c.Gachas[group.OneTimeGachaID]; !ok {
			return fmt.Errorf("gamedata: missing one-time gacha %d", group.OneTimeGachaID)
		}
		c.byGacha[group.OneTimeGachaID] = group.ID
	}
	if group.TenTimeGachaID != 0 {
		if _, ok := c.Gachas[group.TenTimeGachaID]; !ok {
			return fmt.Errorf("gamedata: missing ten-time gacha %d", group.TenTimeGachaID)
		}
		c.byGacha[group.TenTimeGachaID] = group.ID
	}
	if group.FixedID != 0 {
		if fixed.ID != group.FixedID || fixed.CostumeGrade4Count == 0 || fixed.CostumeGrade5Count == 0 {
			return errors.New("gamedata: invalid gacha fixed group")
		}
		c.fixed[fixed.ID] = fixed
	}
	c.groups[group.ID] = group
	return nil
}

func (c *RegularGachaCatalog) GroupForGacha(gachaID uint64) (GachaGroupDesign, bool) {
	if c == nil {
		return GachaGroupDesign{}, false
	}
	groupID, ok := c.byGacha[gachaID]
	if !ok {
		return GachaGroupDesign{}, false
	}
	group, ok := c.groups[groupID]
	return group, ok
}

func (c *RegularGachaCatalog) Group(groupID uint64) (GachaGroupDesign, bool) {
	if c == nil {
		return GachaGroupDesign{}, false
	}
	group, ok := c.groups[groupID]
	return group, ok
}

func (c *RegularGachaCatalog) Fixed(fixedID uint64) (GachaFixedDesign, bool) {
	if c == nil {
		return GachaFixedDesign{}, false
	}
	fixed, ok := c.fixed[fixedID]
	return fixed, ok
}

func (c *RegularGachaCatalog) CostumeGrade(costumeID uint64) (uint64, bool) {
	if c == nil {
		return 0, false
	}
	grade, ok := c.grades[costumeID]
	return grade, ok
}

func (c *RegularGachaCatalog) recordPoolGrades(gacha RegularGacha) {
	if gacha.FixedCostumeID != 0 {
		c.grades[gacha.FixedCostumeID] = 5
	}
	for branch, item := range gacha.Pool {
		grade := uint64(0)
		switch len(gacha.Pool) {
		case 4:
			if branch < 2 {
				grade = 5
			} else {
				grade = uint64(6 - branch) // branch 2/3 => grade 4/3
			}
		case 3:
			grade = uint64(5 - branch)
		}
		if grade != 0 {
			recordCostumeGrade(c.grades, item, grade)
		}
	}
}

func recordCostumeGrade(grades map[uint64]uint64, item WeightedCostume, grade uint64) {
	if item.ID != 0 {
		if current := grades[item.ID]; current == 0 || grade > current {
			grades[item.ID] = grade
		}
		return
	}
	for _, child := range item.Children {
		recordCostumeGrade(grades, child, grade)
	}
}

func (c *RegularGachaCatalog) Character(costumeID uint64) (CharacterDesign, bool) {
	if c == nil {
		return CharacterDesign{}, false
	}
	value, ok := c.characters[costumeID]
	return value, ok
}

func (c *RegularGachaCatalog) Gacha(id uint64) (RegularGacha, bool) {
	if c == nil {
		return RegularGacha{}, false
	}
	value, ok := c.Gachas[id]
	return value, ok
}

func (g RegularGacha) Roll() ([]uint64, error) {
	return g.rollWith(func(limit uint64) (uint64, error) {
		selected, err := rand.Int(rand.Reader, new(big.Int).SetUint64(limit))
		if err != nil {
			return 0, err
		}
		return selected.Uint64(), nil
	})
}

// RollWithCostumeFixed applies the resettable GachaFixedTable counters one
// slot at a time. The ordinary pickup pool layout is pickup-5, other-5,
// grade-4, grade-3; a guaranteed five-star therefore preserves the official
// 50/50 pickup split, while a guaranteed four-star uses the real grade-4 pool.
func (g RegularGacha) RollWithCostumeFixed(previous4, previous5 uint64, fixed GachaFixedDesign) ([]uint64, GachaFixedRoll, error) {
	return g.RollWithCostumeFixedSelection(previous4, previous5, fixed, nil, nil)
}

func (g RegularGacha) RollWithCostumeFixedSelection(previous4, previous5 uint64, fixed GachaFixedDesign, normalSelectedFive, pitySelectedFive []uint64) ([]uint64, GachaFixedRoll, error) {
	return g.rollWithCostumeFixed(previous4, previous5, fixed, normalSelectedFive, pitySelectedFive, func(limit uint64) (uint64, error) {
		selected, err := rand.Int(rand.Reader, new(big.Int).SetUint64(limit))
		if err != nil {
			return 0, err
		}
		return selected.Uint64(), nil
	})
}

func (g RegularGacha) rollWithCostumeFixed(previous4, previous5 uint64, fixed GachaFixedDesign, normalSelectedFive, pitySelectedFive []uint64, draw func(uint64) (uint64, error)) ([]uint64, GachaFixedRoll, error) {
	state := GachaFixedRoll{CostumeGrade4Count: previous4, CostumeGrade5Count: previous5, CostumeGrade4Sort: -1, CostumeGrade5Sort: -1}
	if g.Count <= 0 || (len(g.Pool) != 3 && len(g.Pool) != 4) || fixed.ID == 0 || fixed.CostumeGrade4Count == 0 || fixed.CostumeGrade5Count == 0 || draw == nil {
		return nil, state, errors.New("gamedata: invalid costume fixed gacha")
	}
	fiveBranches := 1
	if len(g.Pool) == 4 {
		fiveBranches = 2
	}
	result := make([]uint64, g.Count)
	for i := range result {
		grade := uint64(0)
		force5 := state.CostumeGrade5Count+1 >= fixed.CostumeGrade5Count
		force4 := !force5 && state.CostumeGrade4Count+1 >= fixed.CostumeGrade4Count
		var id uint64
		var err error
		switch {
		case force5:
			if len(pitySelectedFive) != 0 {
				var selected uint64
				selected, err = draw(uint64(len(pitySelectedFive)))
				if err == nil {
					if selected >= uint64(len(pitySelectedFive)) {
						return nil, state, errors.New("gamedata: selected pity source returned out-of-range value")
					}
					id = pitySelectedFive[selected]
					state.SelectionSorts = append(state.SelectionSorts, i)
				}
			} else {
				id, err = rollCostumeChoiceWith(g.Pool[:fiveBranches], draw)
			}
			grade = 5
			state.CostumeGrade5Sort = i
		case force4:
			id, err = rollCostumeChoiceWith([]WeightedCostume{g.Pool[fiveBranches]}, draw)
			grade = 4
			state.CostumeGrade4Sort = i
		default:
			var branch int
			branch, id, err = rollCostumeBranchWith(g.Pool, draw)
			if branch < fiveBranches {
				grade = 5
				if err == nil && len(normalSelectedFive) != 0 {
					var selected uint64
					selected, err = draw(uint64(len(normalSelectedFive)))
					if err == nil {
						if selected >= uint64(len(normalSelectedFive)) {
							return nil, state, errors.New("gamedata: selected normal source returned out-of-range value")
						}
						id = normalSelectedFive[selected]
						state.SelectionSorts = append(state.SelectionSorts, i)
					}
				}
			} else {
				grade = uint64(4 + fiveBranches - branch)
			}
		}
		if err != nil {
			return nil, state, err
		}
		result[i] = id
		switch grade {
		case 5:
			state.CostumeGrade4Count = 0
			state.CostumeGrade5Count = 0
		case 4:
			state.CostumeGrade4Count = 0
			state.CostumeGrade5Count++
		default:
			state.CostumeGrade4Count++
			state.CostumeGrade5Count++
		}
	}
	return result, state, nil
}

func (g RegularGacha) rollWith(draw func(uint64) (uint64, error)) ([]uint64, error) {
	if g.Count <= 0 || len(g.Pool) == 0 {
		return nil, errors.New("gamedata: invalid regular gacha")
	}
	result := make([]uint64, g.Count)
	start := 0
	if g.FixedCostumeID != 0 {
		result[0] = g.FixedCostumeID
		start = 1
	}
	hasFourOrFive := g.FixedCostumeID != 0
	for i := start; i < len(result); i++ {
		branch, id, err := rollCostumeBranchWith(g.Pool, draw)
		if err != nil {
			return nil, err
		}
		if branch < len(g.Pool)-1 {
			hasFourOrFive = true
		}
		result[i] = id
	}
	// Official costume ten-pulls guarantee grade four or above. The ordinary
	// four-branch pools are ordered as pickup-5, other-5, grade-4, grade-3.
	// If all ten normal rolls landed in grade 3, replace the final slot with
	// an equal-weight draw from the real grade-4 branch.
	if g.FixedCostumeID == 0 && g.Count == 10 && len(g.Pool) == 4 && !hasFourOrFive {
		id, err := rollCostumeChoiceWith([]WeightedCostume{g.Pool[2]}, draw)
		if err != nil {
			return nil, err
		}
		result[len(result)-1] = id
	}
	return result, nil
}

func rollCostumeBranch(pool []WeightedCostume) (int, uint64, error) {
	return rollCostumeBranchWith(pool, func(limit uint64) (uint64, error) {
		selected, err := rand.Int(rand.Reader, new(big.Int).SetUint64(limit))
		if err != nil {
			return 0, err
		}
		return selected.Uint64(), nil
	})
}

func rollCostumeBranchWith(pool []WeightedCostume, draw func(uint64) (uint64, error)) (int, uint64, error) {
	var total uint64
	for _, item := range pool {
		if item.Weight == 0 || math.MaxUint64-total < item.Weight {
			return 0, 0, errors.New("gamedata: invalid regular gacha weight")
		}
		total += item.Weight
	}
	value, err := draw(total)
	if err != nil {
		return 0, 0, err
	}
	if value >= total {
		return 0, 0, errors.New("gamedata: random source returned out-of-range value")
	}
	for i, item := range pool {
		if value < item.Weight {
			id, err := rollCostumeChoiceWith([]WeightedCostume{item}, draw)
			return i, id, err
		}
		value -= item.Weight
	}
	return 0, 0, errors.New("gamedata: regular gacha branch selection failed")
}

func rollCostumeChoice(pool []WeightedCostume) (uint64, error) {
	return rollCostumeChoiceWith(pool, func(limit uint64) (uint64, error) {
		selected, err := rand.Int(rand.Reader, new(big.Int).SetUint64(limit))
		if err != nil {
			return 0, err
		}
		return selected.Uint64(), nil
	})
}

func rollCostumeChoiceWith(pool []WeightedCostume, draw func(uint64) (uint64, error)) (uint64, error) {
	var total uint64
	for _, item := range pool {
		if item.Weight == 0 || math.MaxUint64-total < item.Weight {
			return 0, errors.New("gamedata: invalid regular gacha weight")
		}
		if item.ID == 0 && len(item.Children) == 0 {
			return 0, errors.New("gamedata: empty regular gacha choice")
		}
		total += item.Weight
	}
	value, err := draw(total)
	if err != nil {
		return 0, err
	}
	if value >= total {
		return 0, errors.New("gamedata: random source returned out-of-range value")
	}
	for _, item := range pool {
		if value < item.Weight {
			if item.ID != 0 {
				return item.ID, nil
			}
			return rollCostumeChoiceWith(item.Children, draw)
		}
		value -= item.Weight
	}
	return 0, errors.New("gamedata: regular gacha selection failed")
}

// LoadRegularCostumeGacha loads only the ordinary costume pools currently
// exposed by this local server. Equipment pools remain hidden until complete
// option-instance generation is implemented.
func LoadRegularCostumeGacha(root, version string) (*RegularGachaCatalog, error) {
	infinite, err := LoadInfiniteGacha(root, version)
	if err != nil {
		return nil, err
	}
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-regular-gacha-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "common.db")
	if err := os.WriteFile(path, plain, 0600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	catalog := &RegularGachaCatalog{
		Gachas: map[uint64]RegularGacha{}, characters: make(map[uint64]CharacterDesign),
		groups: map[uint64]GachaGroupDesign{}, byGacha: map[uint64]uint64{}, fixed: map[uint64]GachaFixedDesign{}, grades: map[uint64]uint64{},
	}
	for id, character := range infinite.characters {
		catalog.characters[id] = character
	}
	for _, id := range []uint64{100, 101, 10100084, 11000084, 10100145, 11000145, 10100072, 11000072, 8100118, 8100119, 8100120, 8100121} {
		var proto []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM GachaTable WHERE id=?", id).Scan(&proto); err != nil {
			return nil, fmt.Errorf("gamedata: regular gacha %d: %w", id, err)
		}
		counts, _ := packedInts(proto, 5)
		groups, _ := packedInts(proto, 7)
		prices, _ := packedInts(proto, 10)
		priceTypes, _ := packedInts(proto, 12)
		ticketIDs, _ := packedInts(proto, 8)
		if len(counts) != 1 || len(groups) != 1 || len(prices) != 1 || len(priceTypes) != 1 || (priceTypes[0] != 2 && priceTypes[0] != 3) {
			return nil, fmt.Errorf("gamedata: malformed regular gacha %d", id)
		}
		poolGroupID := groups[0]
		var fixedCostumeID uint64
		if id == 8100121 {
			var root []byte
			if err := db.QueryRow("SELECT ProtoBuf FROM RewardGroupTable WHERE id=?", groups[0]).Scan(&root); err != nil {
				return nil, err
			}
			dropCount, _ := packedInts(root, 1)
			dropType, _ := packedInts(root, 2)
			rootIDs, _ := packedInts(root, 5)
			rootTypes, _ := packedInts(root, 6)
			if len(dropCount) != 1 || dropCount[0] != 10 || len(dropType) != 1 || dropType[0] != 1 || len(rootIDs) != 2 || len(rootTypes) != 2 || rootTypes[0] != 11 || rootTypes[1] != 9 {
				return nil, errors.New("gamedata: malformed fixed pickup ten-pull")
			}
			fixedCostumeID = rootIDs[0]
			poolGroupID = rootIDs[1]
		}
		pool, err := loadCostumeRewardPool(db, poolGroupID)
		if err != nil {
			return nil, fmt.Errorf("gamedata: regular gacha %d: %w", id, err)
		}
		var costumeIDs []uint64
		collectCostumeIDs(pool, &costumeIDs)
		if fixedCostumeID != 0 {
			costumeIDs = append(costumeIDs, fixedCostumeID)
		}
		for _, costumeID := range costumeIDs {
			if _, ok := catalog.characters[costumeID]; ok {
				continue
			}
			character, err := loadGachaCharacterDesign(db, costumeID)
			if err != nil {
				return nil, err
			}
			catalog.characters[costumeID] = character
		}
		if err := validateCostumePool(pool, catalog.characters); err != nil {
			return nil, err
		}
		if fixedCostumeID == 0 {
			var rateError error
			if id == 100 || id == 101 {
				rateError = validateRegularCostumeRates(pool)
			} else {
				rateError = validateOfficialPickupRates(pool)
			}
			if rateError != nil {
				return nil, fmt.Errorf("gamedata: regular gacha %d probability: %w", id, rateError)
			}
		} else if err := validateFixedPickupRemainderRates(pool); err != nil {
			return nil, fmt.Errorf("gamedata: regular gacha %d probability: %w", id, err)
		}
		catalog.Gachas[id] = RegularGacha{ID: id, Count: int(counts[0]), PriceType: priceTypes[0], Price: prices[0], Pool: pool, FixedCostumeID: fixedCostumeID, TicketIDs: ticketIDs}
		catalog.recordPoolGrades(catalog.Gachas[id])
	}
	if err := catalog.loadGroupsAndFixed(db); err != nil {
		return nil, err
	}
	return catalog, nil
}

func validateRegularCostumeRates(pool []WeightedCostume) error {
	if len(pool) != 3 {
		return fmt.Errorf("expected three rarity branches, got %d", len(pool))
	}
	want := [...]uint64{officialFiveStarRate, officialFourStarRate, officialRateScale - officialFiveStarRate - officialFourStarRate}
	for i, item := range pool {
		if item.Weight != want[i] {
			return fmt.Errorf("branch %d weight=%d want=%d", i, item.Weight, want[i])
		}
	}
	return nil
}

func (c *RegularGachaCatalog) loadGroupsAndFixed(db *sql.DB) error {
	rows, err := db.Query("SELECT id,ProtoBuf FROM GachaGroupTable")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id uint64
		var proto []byte
		if err := rows.Scan(&id, &proto); err != nil {
			return err
		}
		readOne := func(field int) uint64 {
			values, _ := packedInts(proto, field)
			if len(values) == 1 {
				return values[0]
			}
			return 0
		}
		group := GachaGroupDesign{
			ID: id, FixedID: readOne(10), PointCount: readOne(27), PickUpExchangeCost: readOne(25),
			PickUpCostumeID: readOne(26), OneTimeGachaID: readOne(24), TenTimeGachaID: readOne(33),
			SelectCount: readOne(29), SelectionChoiceRate: readOne(31), UseSelectionOnlyFixedApply: readOne(36) != 0,
			GachaSubType: readOne(16), IsSelectedFromPity: readOne(21) != 0,
		}
		_, oneLoaded := c.Gachas[group.OneTimeGachaID]
		_, tenLoaded := c.Gachas[group.TenTimeGachaID]
		if !oneLoaded && !tenLoaded {
			continue
		}
		c.groups[id] = group
		if oneLoaded {
			c.byGacha[group.OneTimeGachaID] = id
		}
		if tenLoaded {
			c.byGacha[group.TenTimeGachaID] = id
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, group := range c.groups {
		if group.FixedID == 0 {
			continue
		}
		if _, exists := c.fixed[group.FixedID]; exists {
			continue
		}
		var proto []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM GachaFixedTable WHERE id=?", group.FixedID).Scan(&proto); err != nil {
			return fmt.Errorf("gamedata: gacha fixed %d: %w", group.FixedID, err)
		}
		four, _ := packedInts(proto, 2)
		five, _ := packedInts(proto, 4)
		reset, _ := packedInts(proto, 6)
		if len(four) != 1 || len(five) != 1 || len(reset) != 1 || four[0] == 0 || five[0] == 0 {
			return fmt.Errorf("gamedata: malformed costume gacha fixed %d", group.FixedID)
		}
		c.fixed[group.FixedID] = GachaFixedDesign{ID: group.FixedID, CostumeGrade4Count: four[0], CostumeGrade5Count: five[0], ResetOnMatchingGrade: reset[0] != 0}
	}
	return nil
}

func validateFixedPickupRemainderRates(pool []WeightedCostume) error {
	if len(pool) != 3 {
		return fmt.Errorf("expected three rarity branches, got %d", len(pool))
	}
	want := [...]uint64{officialFiveStarRate, officialFourStarRate, officialRateScale - officialFiveStarRate - officialFourStarRate}
	for i, item := range pool {
		if item.Weight != want[i] {
			return fmt.Errorf("branch %d weight=%d want=%d", i, item.Weight, want[i])
		}
	}
	return nil
}

// validateOfficialPickupRates prevents a GameData/schema regression from
// silently changing the published costume pickup rates. The two five-star
// branches are pickup 1.5% plus the ordinary five-star pool 1.5%.
func validateOfficialPickupRates(pool []WeightedCostume) error {
	if len(pool) != 4 {
		return fmt.Errorf("expected four rarity branches, got %d", len(pool))
	}
	want := [...]uint64{150, 150, officialFourStarRate, officialRateScale - officialFiveStarRate - officialFourStarRate}
	for i, item := range pool {
		if item.Weight != want[i] {
			return fmt.Errorf("branch %d weight=%d want=%d", i, item.Weight, want[i])
		}
	}
	return nil
}

func collectCostumeIDs(pool []WeightedCostume, result *[]uint64) {
	for _, item := range pool {
		if item.ID != 0 {
			*result = append(*result, item.ID)
		} else {
			collectCostumeIDs(item.Children, result)
		}
	}
}

func loadGachaCharacterDesign(db *sql.DB, costumeID uint64) (CharacterDesign, error) {
	characterID := costumeID / 10
	var character []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CharTable WHERE id=?", characterID).Scan(&character); err != nil {
		return CharacterDesign{}, fmt.Errorf("gamedata: costume %d character %d: %w", costumeID, characterID, err)
	}
	growthIDs, _ := packedInts(character, 1)
	baseHP, found, err := fixed64Double(character, 11)
	if err != nil || !found || len(growthIDs) != 1 {
		return CharacterDesign{}, fmt.Errorf("gamedata: character %d invalid base design", characterID)
	}
	var growth []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CharGrowthTable WHERE id=?", growthIDs[0]).Scan(&growth); err != nil {
		return CharacterDesign{}, err
	}
	levelGroups, _ := packedInts(growth, 1)
	if len(levelGroups) != 1 {
		return CharacterDesign{}, fmt.Errorf("gamedata: character %d invalid level group", characterID)
	}
	var level []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CharLevelTable WHERE GroupId=? AND id=1", levelGroups[0]).Scan(&level); err != nil {
		return CharacterDesign{}, err
	}
	ratio, found, err := fixed64Double(level, 6)
	if err != nil || !found {
		return CharacterDesign{}, fmt.Errorf("gamedata: character %d invalid level health", characterID)
	}
	hp := math.Trunc(baseHP * (1 + ratio))
	if hp <= 0 || hp > math.MaxUint64 {
		return CharacterDesign{}, fmt.Errorf("gamedata: character %d invalid HP", characterID)
	}
	costume, err := loadCostumeDesign(db, costumeID)
	if err != nil {
		return CharacterDesign{}, err
	}
	costume.ID = characterID
	costume.HP = uint64(hp)
	return costume, nil
}

// loadCostumeDesign reads the duplicate-upgrade boundary and the official
// post-max exchange directly from CostumeTable/CostumeGrowthTable. Mileage has
// no item ID, so a zero OverflowItemID is valid when OverflowItemType is 20.
func loadCostumeDesign(db *sql.DB, costumeID uint64) (CharacterDesign, error) {
	var costume []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CostumeTable WHERE id=?", costumeID).Scan(&costume); err != nil {
		return CharacterDesign{}, fmt.Errorf("gamedata: costume %d: %w", costumeID, err)
	}
	growthGroups, _ := packedInts(costume, 10)
	maxLevels, _ := packedInts(costume, 15)
	if len(growthGroups) != 1 || len(maxLevels) != 1 || maxLevels[0] == 0 {
		return CharacterDesign{}, fmt.Errorf("gamedata: costume %d invalid growth boundary", costumeID)
	}
	var growth []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CostumeGrowthTable WHERE GroupId=? AND id=?", growthGroups[0], maxLevels[0]).Scan(&growth); err != nil {
		return CharacterDesign{}, fmt.Errorf("gamedata: costume %d max growth: %w", costumeID, err)
	}
	mileageCounts, _ := packedInts(growth, 9)
	mileageIDs, _ := packedInts(growth, 10)
	mileageTypes, _ := packedInts(growth, 11)
	overCounts, _ := packedInts(growth, 12)
	if len(mileageCounts) != 1 || mileageCounts[0] == 0 || len(mileageTypes) != 1 || mileageTypes[0] == 0 || len(overCounts) != 1 || overCounts[0] != maxLevels[0]+1 {
		return CharacterDesign{}, fmt.Errorf("gamedata: costume %d invalid overflow exchange", costumeID)
	}
	var mileageID uint64
	if len(mileageIDs) == 1 {
		mileageID = mileageIDs[0]
	} else if len(mileageIDs) != 0 {
		return CharacterDesign{}, fmt.Errorf("gamedata: costume %d invalid overflow item id", costumeID)
	}
	return CharacterDesign{
		CostumeMaxLevel: maxLevels[0], OverflowItemType: mileageTypes[0],
		OverflowItemID: mileageID, OverflowItemCount: mileageCounts[0],
	}, nil
}

func loadCostumeRewardPool(db *sql.DB, groupID uint64) ([]WeightedCostume, error) {
	var proto []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM RewardGroupTable WHERE id=?", groupID).Scan(&proto); err != nil {
		return nil, err
	}
	ids, _ := packedInts(proto, 5)
	types, _ := packedInts(proto, 6)
	weights, _ := packedInts(proto, 8)
	if len(ids) == 0 || len(ids) != len(types) || len(ids) != len(weights) {
		return nil, errors.New("malformed reward group")
	}
	var result []WeightedCostume
	for i, id := range ids {
		switch types[i] {
		case 11:
			result = append(result, WeightedCostume{ID: id, Weight: weights[i]})
		case 9:
			children, err := loadCostumeRewardPool(db, id)
			if err != nil {
				return nil, err
			}
			result = append(result, WeightedCostume{Weight: weights[i], Children: children})
		default:
			return nil, fmt.Errorf("unsupported costume reward type %d", types[i])
		}
	}
	return result, nil
}

func validateCostumePool(pool []WeightedCostume, characters map[uint64]CharacterDesign) error {
	if len(pool) == 0 {
		return errors.New("gamedata: empty costume pool")
	}
	for _, item := range pool {
		if item.ID != 0 {
			if _, ok := characters[item.ID]; !ok {
				return fmt.Errorf("gamedata: regular gacha costume %d lacks character design", item.ID)
			}
			continue
		}
		if err := validateCostumePool(item.Children, characters); err != nil {
			return err
		}
	}
	return nil
}

func NewInfiniteGachaDesign(count int, costumeIDs []uint64, characters map[uint64]CharacterDesign) (*InfiniteGachaDesign, error) {
	return NewInfiniteGachaDesignWithRates(count, costumeIDs, costumeIDs, costumeIDs, characters)
}

// NewInfiniteGachaDesignWithRates builds the local reroll design. Its first
// Count-1 slots use the official costume base rates (3%/14%/83%), while the
// last slot is a five-star guarantee chosen uniformly from the real eligible
// five-star pool.
func NewInfiniteGachaDesignWithRates(count int, fiveStarIDs, fourStarIDs, threeStarIDs []uint64, characters map[uint64]CharacterDesign) (*InfiniteGachaDesign, error) {
	if count <= 0 || len(fiveStarIDs) == 0 || len(fourStarIDs) == 0 || len(threeStarIDs) == 0 || len(characters) == 0 {
		return nil, fmt.Errorf("gamedata: invalid infinite gacha design")
	}
	design := &InfiniteGachaDesign{
		Count: count, CostumeIDs: append([]uint64(nil), fiveStarIDs...),
		FiveStarIDs: append([]uint64(nil), fiveStarIDs...), FourStarIDs: append([]uint64(nil), fourStarIDs...),
		ThreeStarIDs: append([]uint64(nil), threeStarIDs...), characters: make(map[uint64]CharacterDesign, len(characters)),
	}
	for _, pool := range [][]uint64{design.FiveStarIDs, design.FourStarIDs, design.ThreeStarIDs} {
		for _, id := range pool {
			character, ok := characters[id]
			if id == 0 || !ok || character.ID == 0 || character.HP == 0 {
				return nil, fmt.Errorf("gamedata: costume %d has no valid character", id)
			}
			design.characters[id] = character
		}
	}
	return design, nil
}

func LoadInfiniteGacha(root, version string) (*InfiniteGachaDesign, error) {
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-gacha-")
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

	var gacha []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM GachaTable WHERE id=?", InfiniteGachaID).Scan(&gacha); err != nil {
		return nil, fmt.Errorf("gamedata: infinite gacha: %w", err)
	}
	count, _ := packedInts(gacha, 5)
	reward, _ := packedInts(gacha, 7)
	priceCount, _ := packedInts(gacha, 10)
	if len(count) != 1 || count[0] != 10 || len(reward) != 1 || reward[0] != InfiniteGachaID || len(priceCount) != 0 {
		return nil, fmt.Errorf("gamedata: unexpected infinite gacha definition count=%v reward=%v price=%v", count, reward, priceCount)
	}
	var cash []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CashProductTable WHERE id=? AND GroupId=?", InfiniteProductID, InfiniteProductGroupID).Scan(&cash); err != nil {
		return nil, fmt.Errorf("gamedata: infinite product: %w", err)
	}
	productID, _ := packedInts(cash, 6)
	groupID, _ := packedInts(cash, 5)
	if len(productID) != 1 || productID[0] != InfiniteProductID || len(groupID) != 1 || groupID[0] != InfiniteProductGroupID {
		return nil, fmt.Errorf("gamedata: unexpected infinite product identity")
	}

	// GachaTable.FixedGachaRewardId=9100035 is the official costume pool used
	// by the observed infinite previews. RewardGroup item type 11 is Costume.
	var pool []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM RewardGroupTable WHERE id=9100035").Scan(&pool); err != nil {
		return nil, fmt.Errorf("gamedata: infinite costume pool: %w", err)
	}
	ids, err := packedInts(pool, 5)
	if err != nil {
		return nil, err
	}
	types, err := packedInts(pool, 6)
	if err != nil || len(ids) == 0 || len(ids) != len(types) {
		return nil, fmt.Errorf("gamedata: malformed infinite costume pool")
	}
	characters := make(map[uint64]CharacterDesign)
	// CharLevelTable.Health is a growth ratio, not an absolute HP value. The
	// client computes level-one HP as trunc(CharTable.HealthValue * (1+ratio)).
	// Keeping the two sources separate also avoids rejecting valid fractional
	// ratios such as group 101's 0.2.
	levelOneHealthRatio := make(map[uint64]float64)
	rows, err := db.Query("SELECT ProtoBuf FROM CharLevelTable")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var levelProto []byte
		if err := rows.Scan(&levelProto); err != nil {
			rows.Close()
			return nil, err
		}
		groups, _ := packedInts(levelProto, 5)
		levels, _ := packedInts(levelProto, 7)
		health, healthFound, healthErr := fixed64Double(levelProto, 6)
		if healthErr != nil {
			rows.Close()
			return nil, healthErr
		}
		if len(groups) == 1 && len(levels) == 1 && levels[0] == 1 && healthFound && health >= 0 && !math.IsNaN(health) && !math.IsInf(health, 0) {
			levelOneHealthRatio[groups[0]] = health
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i, costumeID := range ids {
		if types[i] != 11 || costumeID == 0 {
			return nil, fmt.Errorf("gamedata: infinite pool entry %d has type %d", costumeID, types[i])
		}
		characterID := costumeID / 10
		var character []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM CharTable WHERE id=?", characterID).Scan(&character); err != nil {
			return nil, fmt.Errorf("gamedata: costume %d character %d: %w", costumeID, characterID, err)
		}
		growthID, _ := packedInts(character, 1)
		if len(growthID) != 1 {
			return nil, fmt.Errorf("gamedata: character %d has invalid growth %v", characterID, growthID)
		}
		baseHP, baseHPFound, err := fixed64Double(character, 11)
		if err != nil {
			return nil, fmt.Errorf("gamedata: character %d base HP: %w", characterID, err)
		}
		if !baseHPFound || baseHP <= 0 || math.IsNaN(baseHP) || math.IsInf(baseHP, 0) {
			return nil, fmt.Errorf("gamedata: character %d has invalid base HP %v", characterID, baseHP)
		}
		var growth []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM CharGrowthTable WHERE id=?", growthID[0]).Scan(&growth); err != nil {
			return nil, fmt.Errorf("gamedata: character %d growth %d: %w", characterID, growthID[0], err)
		}
		levelGroup, _ := packedInts(growth, 1)
		if len(levelGroup) != 1 {
			return nil, fmt.Errorf("gamedata: character %d has invalid level group %v", characterID, levelGroup)
		}
		healthRatio, found := levelOneHealthRatio[levelGroup[0]]
		if !found {
			return nil, fmt.Errorf("gamedata: character %d has invalid level group %v", characterID, levelGroup)
		}
		hp := math.Trunc(baseHP * (1 + healthRatio))
		if hp <= 0 || hp > math.MaxUint64 {
			return nil, fmt.Errorf("gamedata: character %d computes invalid level-one HP %v", characterID, hp)
		}
		costume, err := loadCostumeDesign(db, costumeID)
		if err != nil {
			return nil, err
		}
		costume.ID = characterID
		costume.HP = uint64(hp)
		characters[costumeID] = costume
	}
	fourStarPool, err := loadCostumeRewardPool(db, 70008)
	if err != nil {
		return nil, fmt.Errorf("gamedata: infinite four-star pool: %w", err)
	}
	threeStarPool, err := loadCostumeRewardPool(db, 70004)
	if err != nil {
		return nil, fmt.Errorf("gamedata: infinite three-star pool: %w", err)
	}
	var fourStarIDs, threeStarIDs []uint64
	collectCostumeIDs(fourStarPool, &fourStarIDs)
	collectCostumeIDs(threeStarPool, &threeStarIDs)
	if len(ids) != 129 || len(fourStarIDs) != 13 || len(threeStarIDs) != 16 {
		return nil, fmt.Errorf("gamedata: unexpected infinite rarity pools five=%d four=%d three=%d", len(ids), len(fourStarIDs), len(threeStarIDs))
	}
	for _, costumeID := range append(append([]uint64(nil), fourStarIDs...), threeStarIDs...) {
		if _, exists := characters[costumeID]; exists {
			continue
		}
		character, err := loadGachaCharacterDesign(db, costumeID)
		if err != nil {
			return nil, err
		}
		characters[costumeID] = character
	}
	return NewInfiniteGachaDesignWithRates(int(count[0]), ids, fourStarIDs, threeStarIDs, characters)
}

func fixed64Double(proto []byte, number int) (float64, bool, error) {
	var result float64
	found := false
	err := wire.Walk(proto, func(field wire.Field) error {
		if field.Number != number {
			return nil
		}
		if field.Type != 1 || len(field.Value) != 8 || found {
			return fmt.Errorf("gamedata: invalid double field %d", number)
		}
		result = math.Float64frombits(binary.LittleEndian.Uint64(field.Value))
		found = true
		return nil
	})
	return result, found, err
}

func (d *InfiniteGachaDesign) Roll() ([]uint64, error) {
	return d.rollWith(func(limit uint64) (uint64, error) {
		selected, err := rand.Int(rand.Reader, new(big.Int).SetUint64(limit))
		if err != nil {
			return 0, err
		}
		return selected.Uint64(), nil
	})
}

func (d *InfiniteGachaDesign) rollWith(draw func(uint64) (uint64, error)) ([]uint64, error) {
	if d == nil || d.Count <= 0 || len(d.FiveStarIDs) == 0 || len(d.FourStarIDs) == 0 || len(d.ThreeStarIDs) == 0 || draw == nil {
		return nil, fmt.Errorf("gamedata: invalid infinite gacha design")
	}
	result := make([]uint64, d.Count)
	for i := 0; i < d.Count-1; i++ {
		rate, err := draw(officialRateScale)
		if err != nil {
			return nil, err
		}
		var pool []uint64
		switch {
		case rate < officialFiveStarRate:
			pool = d.FiveStarIDs
		case rate < officialFiveStarRate+officialFourStarRate:
			pool = d.FourStarIDs
		default:
			pool = d.ThreeStarIDs
		}
		selected, err := draw(uint64(len(pool)))
		if err != nil {
			return nil, err
		}
		if selected >= uint64(len(pool)) {
			return nil, errors.New("gamedata: random source returned out-of-range value")
		}
		result[i] = pool[selected]
	}
	selected, err := draw(uint64(len(d.FiveStarIDs)))
	if err != nil {
		return nil, err
	}
	if selected >= uint64(len(d.FiveStarIDs)) {
		return nil, errors.New("gamedata: random source returned out-of-range value")
	}
	result[d.Count-1] = d.FiveStarIDs[selected]
	return result, nil
}

func (d *InfiniteGachaDesign) Character(costumeID uint64) (CharacterDesign, bool) {
	if d == nil {
		return CharacterDesign{}, false
	}
	character, ok := d.characters[costumeID]
	return character, ok
}
