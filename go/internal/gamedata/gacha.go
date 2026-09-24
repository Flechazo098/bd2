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
	ID                      uint64
	Count                   int
	DailyPayGachaCount      uint64
	DailyPayGachaPriceCount uint64
	FreeCountDay            uint64
	PriceType               uint64
	PriceID                 uint64
	Price                   uint64
	Pool                    []WeightedCostume
	RewardGroup             *CostumeRewardGroup
	FixedCostumeID          uint64
	TicketIDs               []uint64
}

type GachaGroupDesign struct {
	ID                         uint64
	GachaType                  uint64
	BuyLimitCount              uint64
	CashProductGroupID         uint64
	CashProductID              uint64
	CashSalesGroup             uint64
	FixedID                    uint64
	PointCount                 uint64
	PickUpExchangeCost         uint64
	PickUpCostumeID            uint64
	OneTimeGachaID             uint64
	TenTimeGachaID             uint64
	SelectCount                uint64
	SelectionChangeCount       uint64
	SelectionChoiceRate        uint64
	UseSelectionOnlyFixedApply bool
	GachaSubType               uint64
	IsSelectedFromPity         bool
}

// CostumeRewardGroup preserves RewardGroupTable's execution structure.  A
// zero DropType repeats one weighted choice DropCount times; DropType 1 emits
// every entry once.  Keeping that distinction is required for selection and
// step-up gachas whose root combines several independently-sized child groups.
type CostumeRewardGroup struct {
	ID        uint64
	DropCount uint64
	DropType  uint64
	Entries   []CostumeRewardEntry
}

type CostumeRewardEntry struct {
	ItemType uint64
	ItemID   uint64
	Count    uint64
	Weight   uint64
	Group    *CostumeRewardGroup
}

type GachaStepDesign struct {
	Step               uint64
	GroupID            uint64
	GachaID            uint64
	FixedID            uint64
	IsDisplayFixedItem uint64
}

type GachaStepUpDesign struct {
	ID    uint64
	Steps []GachaStepDesign
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
	Gachas      map[uint64]RegularGacha
	characters  map[uint64]CharacterDesign
	groups      map[uint64]GachaGroupDesign
	byGacha     map[uint64]uint64
	fixed       map[uint64]GachaFixedDesign
	grades      map[uint64]uint64
	stepUps     map[uint64]GachaStepUpDesign
	stepByGacha map[uint64]GachaStepDesign
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
		stepUps: map[uint64]GachaStepUpDesign{}, stepByGacha: map[uint64]GachaStepDesign{},
	}
	for id, character := range characters {
		catalog.characters[id] = character
	}
	for id, gacha := range gachas {
		if id == 0 || gacha.ID != id || gacha.Count <= 0 || (gacha.PriceType != 2 && gacha.PriceType != 3 && gacha.PriceType != 19) || gacha.Price == 0 {
			return nil, errors.New("gamedata: invalid regular gacha definition")
		}
		if gacha.RewardGroup == nil {
			if err := validateCostumePool(gacha.Pool, catalog.characters); err != nil {
				return nil, err
			}
		} else if count, err := costumeRewardCount(gacha.RewardGroup); err != nil || count != uint64(gacha.Count) {
			return nil, fmt.Errorf("gamedata: invalid regular gacha reward program count=%d want=%d: %w", count, gacha.Count, err)
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
		if fixed.ID != group.FixedID || (fixed.CostumeGrade4Count == 0 && fixed.CostumeGrade5Count == 0) {
			return errors.New("gamedata: invalid gacha fixed group")
		}
		c.fixed[fixed.ID] = fixed
	}
	c.groups[group.ID] = group
	return nil
}

func (c *RegularGachaCatalog) AddStepUpDesign(stepUp GachaStepUpDesign) error {
	if c == nil || stepUp.ID == 0 || len(stepUp.Steps) == 0 {
		return errors.New("gamedata: invalid gacha step-up")
	}
	if c.stepUps == nil {
		c.stepUps = map[uint64]GachaStepUpDesign{}
	}
	if c.stepByGacha == nil {
		c.stepByGacha = map[uint64]GachaStepDesign{}
	}
	copyValue := GachaStepUpDesign{ID: stepUp.ID, Steps: append([]GachaStepDesign(nil), stepUp.Steps...)}
	for i, step := range copyValue.Steps {
		if step.Step != uint64(i+1) || step.GroupID == 0 || step.GachaID == 0 {
			return errors.New("gamedata: invalid gacha step-up step")
		}
		if _, ok := c.Gachas[step.GachaID]; !ok {
			return fmt.Errorf("gamedata: missing step-up gacha %d", step.GachaID)
		}
		if existing, ok := c.stepByGacha[step.GachaID]; ok && existing != step {
			return fmt.Errorf("gamedata: step-up gacha %d is already registered", step.GachaID)
		}
	}
	c.stepUps[stepUp.ID] = copyValue
	for _, step := range copyValue.Steps {
		c.stepByGacha[step.GachaID] = step
	}
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

func (c *RegularGachaCatalog) Groups() []GachaGroupDesign {
	if c == nil {
		return nil
	}
	out := make([]GachaGroupDesign, 0, len(c.groups))
	for _, group := range c.groups {
		out = append(out, group)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (c *RegularGachaCatalog) StepUp(groupID uint64) (GachaStepUpDesign, bool) {
	if c == nil {
		return GachaStepUpDesign{}, false
	}
	value, ok := c.stepUps[groupID]
	if ok {
		value.Steps = append([]GachaStepDesign(nil), value.Steps...)
	}
	return value, ok
}

func (c *RegularGachaCatalog) StepUps() []GachaStepUpDesign {
	if c == nil {
		return nil
	}
	out := make([]GachaStepUpDesign, 0, len(c.stepUps))
	for id := range c.stepUps {
		value, _ := c.StepUp(id)
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (c *RegularGachaCatalog) StepForGacha(gachaID uint64) (GachaStepDesign, bool) {
	if c == nil {
		return GachaStepDesign{}, false
	}
	value, ok := c.stepByGacha[gachaID]
	return value, ok
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

func (c *RegularGachaCatalog) SpecialSelectionIDs(gachaID uint64) []uint64 {
	gacha, ok := c.Gacha(gachaID)
	if !ok || gacha.RewardGroup == nil || gacha.RewardGroup.DropType != 1 || len(gacha.RewardGroup.Entries) == 0 {
		return nil
	}
	choice := gacha.RewardGroup.Entries[0].Group
	if choice == nil || choice.DropType != 0 || choice.DropCount == 0 {
		return nil
	}
	result := make([]uint64, 0, len(choice.Entries))
	for _, entry := range choice.Entries {
		if entry.Group != nil || entry.ItemType != 11 || entry.ItemID == 0 || entry.Count != 1 || entry.Weight == 0 {
			return nil
		}
		result = append(result, entry.ItemID)
	}
	return result
}

func (c *RegularGachaCatalog) SpecialSelectionCount(gachaID uint64) uint64 {
	gacha, ok := c.Gacha(gachaID)
	if !ok || gacha.RewardGroup == nil || gacha.RewardGroup.DropType != 1 || len(gacha.RewardGroup.Entries) == 0 || gacha.RewardGroup.Entries[0].Group == nil {
		return 0
	}
	return gacha.RewardGroup.Entries[0].Group.DropCount
}

func (g RegularGacha) Roll() ([]uint64, error) {
	if g.RewardGroup != nil {
		return g.rollRewardGroup(func(limit uint64) (uint64, error) {
			selected, err := rand.Int(rand.Reader, new(big.Int).SetUint64(limit))
			if err != nil {
				return 0, err
			}
			return selected.Uint64(), nil
		})
	}
	return g.rollWith(func(limit uint64) (uint64, error) {
		selected, err := rand.Int(rand.Reader, new(big.Int).SetUint64(limit))
		if err != nil {
			return 0, err
		}
		return selected.Uint64(), nil
	})
}

// RollSpecialSelection executes a composite selection-gacha program while
// replacing its leading five-star pool with the player's saved choices. The
// current Moonrise design is intentionally validated by structure rather than
// table IDs: a sequential root first emits the selected rewards and then
// executes the remaining GameData reward branches unchanged.
func (g RegularGacha) RollSpecialSelection(selected []uint64) ([]uint64, error) {
	return g.rollSpecialSelection(selected, func(limit uint64) (uint64, error) {
		value, err := rand.Int(rand.Reader, new(big.Int).SetUint64(limit))
		if err != nil {
			return 0, err
		}
		return value.Uint64(), nil
	})
}

func (g RegularGacha) rollSpecialSelection(selected []uint64, draw func(uint64) (uint64, error)) ([]uint64, error) {
	root := g.RewardGroup
	if g.Count <= 0 || root == nil || root.DropType != 1 || len(root.Entries) < 2 || draw == nil || len(selected) == 0 {
		return nil, errors.New("gamedata: invalid special selection gacha")
	}
	choice := root.Entries[0].Group
	if choice == nil || choice.DropType != 0 || choice.DropCount == 0 {
		return nil, errors.New("gamedata: special selection reward has no weighted leading pool")
	}
	eligible := make(map[uint64]bool, len(choice.Entries))
	for _, entry := range choice.Entries {
		if entry.Group != nil || entry.ItemType != 11 || entry.ItemID == 0 || entry.Count != 1 || entry.Weight == 0 {
			return nil, errors.New("gamedata: malformed special selection pool")
		}
		eligible[entry.ItemID] = true
	}
	seen := make(map[uint64]bool, len(selected))
	for _, id := range selected {
		if id == 0 || seen[id] || !eligible[id] {
			return nil, fmt.Errorf("gamedata: invalid special selection costume %d", id)
		}
		seen[id] = true
	}
	result := make([]uint64, 0, g.Count)
	for i := uint64(0); i < choice.DropCount; i++ {
		index, err := draw(uint64(len(selected)))
		if err != nil {
			return nil, err
		}
		if index >= uint64(len(selected)) {
			return nil, errors.New("gamedata: special selection random source returned out-of-range value")
		}
		result = append(result, selected[index])
	}
	for _, entry := range root.Entries[1:] {
		if entry.Group == nil {
			if entry.ItemType != 11 || entry.ItemID == 0 || entry.Count == 0 {
				return nil, errors.New("gamedata: malformed special selection remainder")
			}
			for i := uint64(0); i < entry.Count; i++ {
				result = append(result, entry.ItemID)
			}
			continue
		}
		items, err := rollCostumeRewardGroup(entry.Group, draw)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
	}
	if len(result) != g.Count {
		return nil, fmt.Errorf("gamedata: special selection returned %d items, want %d", len(result), g.Count)
	}
	return result, nil
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
	if g.Count <= 0 || (len(g.Pool) != 3 && len(g.Pool) != 4) || fixed.ID == 0 || (fixed.CostumeGrade4Count == 0 && fixed.CostumeGrade5Count == 0) || draw == nil {
		return nil, state, errors.New("gamedata: invalid costume fixed gacha")
	}
	fiveBranches := 1
	if len(g.Pool) == 4 {
		fiveBranches = 2
	}
	result := make([]uint64, g.Count)
	for i := range result {
		grade := uint64(0)
		force5 := fixed.CostumeGrade5Count != 0 && state.CostumeGrade5Count+1 >= fixed.CostumeGrade5Count
		force4 := !force5 && fixed.CostumeGrade4Count != 0 && state.CostumeGrade4Count+1 >= fixed.CostumeGrade4Count
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

func (g RegularGacha) rollRewardGroup(draw func(uint64) (uint64, error)) ([]uint64, error) {
	if g.Count <= 0 || g.RewardGroup == nil || draw == nil {
		return nil, errors.New("gamedata: invalid costume reward program")
	}
	result, err := rollCostumeRewardGroup(g.RewardGroup, draw)
	if err != nil {
		return nil, err
	}
	if len(result) != g.Count {
		return nil, fmt.Errorf("gamedata: costume reward program returned %d items, want %d", len(result), g.Count)
	}
	return result, nil
}

func rollCostumeRewardGroup(group *CostumeRewardGroup, draw func(uint64) (uint64, error)) ([]uint64, error) {
	if group == nil || group.DropCount == 0 || len(group.Entries) == 0 {
		return nil, errors.New("gamedata: malformed costume reward group")
	}
	emit := func(entry CostumeRewardEntry) ([]uint64, error) {
		if entry.Group != nil {
			return rollCostumeRewardGroup(entry.Group, draw)
		}
		if entry.ItemType != 11 || entry.ItemID == 0 || entry.Count == 0 {
			return nil, errors.New("gamedata: malformed direct costume reward")
		}
		out := make([]uint64, entry.Count)
		for i := range out {
			out[i] = entry.ItemID
		}
		return out, nil
	}
	var out []uint64
	switch group.DropType {
	case 0:
		var total uint64
		for _, entry := range group.Entries {
			if entry.Weight == 0 || math.MaxUint64-total < entry.Weight {
				return nil, errors.New("gamedata: invalid costume reward weight")
			}
			total += entry.Weight
		}
		for i := uint64(0); i < group.DropCount; i++ {
			value, err := draw(total)
			if err != nil {
				return nil, err
			}
			if value >= total {
				return nil, errors.New("gamedata: random source returned out-of-range value")
			}
			for _, entry := range group.Entries {
				if value < entry.Weight {
					items, err := emit(entry)
					if err != nil {
						return nil, err
					}
					out = append(out, items...)
					break
				}
				value -= entry.Weight
			}
		}
	case 1:
		for _, entry := range group.Entries {
			items, err := emit(entry)
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
		}
	default:
		return nil, fmt.Errorf("gamedata: unsupported costume reward drop type %d", group.DropType)
	}
	return out, nil
}

func costumeRewardCount(group *CostumeRewardGroup) (uint64, error) {
	if group == nil || group.DropCount == 0 || len(group.Entries) == 0 {
		return 0, errors.New("malformed costume reward group")
	}
	entryCount := func(entry CostumeRewardEntry) (uint64, error) {
		if entry.Group != nil {
			return costumeRewardCount(entry.Group)
		}
		if entry.ItemType != 11 || entry.ItemID == 0 || entry.Count == 0 {
			return 0, errors.New("malformed direct costume reward")
		}
		return entry.Count, nil
	}
	if group.DropType == 0 {
		var each uint64
		for _, entry := range group.Entries {
			count, err := entryCount(entry)
			if err != nil {
				return 0, err
			}
			if each == 0 {
				each = count
			} else if each != count {
				return 0, errors.New("weighted costume reward entries have different cardinality")
			}
		}
		if math.MaxUint64/group.DropCount < each {
			return 0, errors.New("costume reward count overflow")
		}
		return group.DropCount * each, nil
	}
	if group.DropType != 1 {
		return 0, fmt.Errorf("unsupported costume reward drop type %d", group.DropType)
	}
	var total uint64
	for _, entry := range group.Entries {
		count, err := entryCount(entry)
		if err != nil || math.MaxUint64-total < count {
			return 0, errors.New("invalid costume reward count")
		}
		total += count
	}
	return total, nil
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

// LoadRegularCostumeGacha retains the historical catalog used by existing
// callers. New schedule code should pass the captured active group IDs to
// LoadRegularCostumeGachaGroups; GameData defines a group but does not say that
// it is currently open.
func LoadRegularCostumeGacha(root, version string) (*RegularGachaCatalog, error) {
	return LoadRegularCostumeGachaGroups(root, version, []uint64{10001, 135, 205, 121}, []uint64{29})
}

func LoadActiveGacha(root, version string, costumeGroupIDs, equipmentGroupIDs, stepUpGroupIDs []uint64) (*RegularGachaCatalog, *EquipmentGachaCatalog, error) {
	costumes, err := LoadRegularCostumeGachaGroups(root, version, costumeGroupIDs, stepUpGroupIDs)
	if err != nil {
		return nil, nil, err
	}
	equipment, err := LoadEquipmentGachaGroups(root, version, equipmentGroupIDs)
	if err != nil {
		return nil, nil, err
	}
	return costumes, equipment, nil
}

func LoadActiveGachaForSchedules(root, version string, scheduleGroupIDs, stepUpGroupIDs []uint64) (*RegularGachaCatalog, *EquipmentGachaCatalog, error) {
	costume, equipment, err := ClassifyActiveGachaGroups(root, version, scheduleGroupIDs)
	if err != nil {
		return nil, nil, err
	}
	return LoadActiveGacha(root, version, costume, equipment, stepUpGroupIDs)
}

// ClassifyActiveGachaGroups reads static type metadata for groups already
// selected by a captured schedule. Resemara is owned by LoadInfiniteGacha and
// is intentionally omitted from both ordinary catalogs.
func ClassifyActiveGachaGroups(root, version string, groupIDs []uint64) (costume, equipment []uint64, err error) {
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-gacha-classify-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "common.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		return nil, nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	seen := map[uint64]bool{}
	for _, id := range groupIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		var raw []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM GachaGroupTable WHERE id=?", id).Scan(&raw); err != nil {
			return nil, nil, fmt.Errorf("gamedata: active gacha group %d: %w", id, err)
		}
		types, _ := packedInts(raw, 17)
		subTypes, _ := packedInts(raw, 16)
		if len(types) != 1 || len(subTypes) > 1 {
			return nil, nil, fmt.Errorf("gamedata: active gacha group %d has malformed type", id)
		}
		subType := uint64(0)
		if len(subTypes) == 1 {
			subType = subTypes[0]
		}
		if types[0] == 1 && subType == 5 {
			continue
		}
		switch types[0] {
		case 1:
			costume = append(costume, id)
		case 2:
			equipment = append(equipment, id)
		default:
			return nil, nil, fmt.Errorf("gamedata: active gacha group %d has unsupported type %d", id, types[0])
		}
	}
	sort.Slice(costume, func(i, j int) bool { return costume[i] < costume[j] })
	sort.Slice(equipment, func(i, j int) bool { return equipment[i] < equipment[j] })
	return costume, equipment, nil
}

func LoadRegularCostumeGachaGroups(root, version string, groupIDs, stepUpGroupIDs []uint64) (*RegularGachaCatalog, error) {
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
		stepUps: map[uint64]GachaStepUpDesign{}, stepByGacha: map[uint64]GachaStepDesign{},
	}
	for id, character := range infinite.characters {
		catalog.characters[id] = character
	}
	gachaIDs := make(map[uint64]struct{})
	for _, groupID := range groupIDs {
		var raw []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM GachaGroupTable WHERE id=?", groupID).Scan(&raw); err != nil {
			return nil, fmt.Errorf("gamedata: costume gacha group %d: %w", groupID, err)
		}
		types, _ := packedInts(raw, 17)
		if len(types) != 1 || types[0] != 1 {
			return nil, fmt.Errorf("gamedata: group %d is not a costume gacha", groupID)
		}
		for _, field := range []int{24, 33} {
			ids, _ := packedInts(raw, field)
			if len(ids) > 1 {
				return nil, fmt.Errorf("gamedata: group %d has malformed gacha field %d", groupID, field)
			}
			if len(ids) == 1 && ids[0] != 0 {
				gachaIDs[ids[0]] = struct{}{}
			}
		}
	}
	for _, stepUpID := range stepUpGroupIDs {
		stepUp, err := loadGachaStepUp(db, stepUpID)
		if err != nil {
			return nil, err
		}
		catalog.stepUps[stepUpID] = stepUp
		for _, step := range stepUp.Steps {
			if _, exists := catalog.stepByGacha[step.GachaID]; exists {
				return nil, fmt.Errorf("gamedata: step-up gacha %d belongs to multiple groups", step.GachaID)
			}
			catalog.stepByGacha[step.GachaID] = step
			gachaIDs[step.GachaID] = struct{}{}
		}
	}
	orderedIDs := make([]uint64, 0, len(gachaIDs))
	for id := range gachaIDs {
		orderedIDs = append(orderedIDs, id)
	}
	sort.Slice(orderedIDs, func(i, j int) bool { return orderedIDs[i] < orderedIDs[j] })
	for _, id := range orderedIDs {
		var proto []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM GachaTable WHERE id=?", id).Scan(&proto); err != nil {
			return nil, fmt.Errorf("gamedata: regular gacha %d: %w", id, err)
		}
		counts, _ := packedInts(proto, 5)
		dailyPayCounts, _ := packedInts(proto, 1)
		dailyPayPrices, _ := packedInts(proto, 2)
		freeCounts, _ := packedInts(proto, 4)
		groups, _ := packedInts(proto, 7)
		prices, _ := packedInts(proto, 10)
		priceIDs, _ := packedInts(proto, 11)
		priceTypes, _ := packedInts(proto, 12)
		ticketIDs, _ := packedInts(proto, 8)
		if len(counts) != 1 || len(groups) != 1 || len(prices) != 1 || len(priceTypes) != 1 ||
			(priceTypes[0] != 2 && priceTypes[0] != 3 && priceTypes[0] != 19) {
			return nil, fmt.Errorf("gamedata: malformed regular gacha %d", id)
		}
		var priceID uint64
		if len(priceIDs) == 1 {
			priceID = priceIDs[0]
		} else if len(priceIDs) != 0 {
			return nil, fmt.Errorf("gamedata: regular gacha %d has malformed price id", id)
		}
		reward, err := loadCostumeRewardGroup(db, groups[0], map[uint64]bool{})
		if err != nil {
			return nil, fmt.Errorf("gamedata: regular gacha %d: %w", id, err)
		}
		rewardCount, err := costumeRewardCount(reward)
		if err != nil || rewardCount != counts[0] {
			return nil, fmt.Errorf("gamedata: regular gacha %d reward count=%d want=%d: %w", id, rewardCount, counts[0], err)
		}
		pool, _ := costumeRewardPool(reward)
		fixedCostumeID, remainder := fixedCostumeReward(reward)
		if fixedCostumeID != 0 {
			pool = remainder
		}
		var costumeIDs []uint64
		collectCostumeRewardIDs(reward, &costumeIDs)
		for _, costumeID := range costumeIDs {
			if _, ok := catalog.characters[costumeID]; !ok {
				character, err := loadGachaCharacterDesign(db, costumeID)
				if err != nil {
					return nil, err
				}
				catalog.characters[costumeID] = character
			}
			grade, err := loadCostumeGrade(db, costumeID)
			if err != nil {
				return nil, err
			}
			catalog.grades[costumeID] = grade
		}
		if len(pool) != 0 {
			if err := validateCostumePool(pool, catalog.characters); err != nil {
				return nil, err
			}
		}
		if len(pool) == 4 {
			err = validateOfficialPickupRates(pool)
		} else if len(pool) == 3 {
			err = validateRegularCostumeRates(pool)
		} else if reward.DropType == 0 {
			err = fmt.Errorf("expected three or four rarity branches, got %d", len(pool))
		}
		if err != nil {
			return nil, fmt.Errorf("gamedata: regular gacha %d probability: %w", id, err)
		}
		var program *CostumeRewardGroup
		if reward.DropType != 0 {
			program = reward
		}
		var dailyPayCount, dailyPayPrice, freeCount uint64
		if len(dailyPayCounts) == 1 {
			dailyPayCount = dailyPayCounts[0]
		} else if len(dailyPayCounts) != 0 {
			return nil, fmt.Errorf("gamedata: regular gacha %d has malformed daily pay count", id)
		}
		if len(dailyPayPrices) == 1 {
			dailyPayPrice = dailyPayPrices[0]
		} else if len(dailyPayPrices) != 0 {
			return nil, fmt.Errorf("gamedata: regular gacha %d has malformed daily pay price", id)
		}
		if len(freeCounts) == 1 {
			freeCount = freeCounts[0]
		} else if len(freeCounts) != 0 {
			return nil, fmt.Errorf("gamedata: regular gacha %d has malformed daily free count", id)
		}
		if (dailyPayCount == 0) != (dailyPayPrice == 0) {
			return nil, fmt.Errorf("gamedata: regular gacha %d has incomplete daily paid design", id)
		}
		catalog.Gachas[id] = RegularGacha{ID: id, Count: int(counts[0]), DailyPayGachaCount: dailyPayCount, DailyPayGachaPriceCount: dailyPayPrice, FreeCountDay: freeCount, PriceType: priceTypes[0], PriceID: priceID, Price: prices[0], Pool: pool, RewardGroup: program, FixedCostumeID: fixedCostumeID, TicketIDs: ticketIDs}
		catalog.recordPoolGrades(catalog.Gachas[id])
	}
	if err := catalog.loadGroupsAndFixed(db); err != nil {
		return nil, err
	}
	if err := catalog.loadStepFixed(db); err != nil {
		return nil, err
	}
	return catalog, nil
}

func loadGachaStepUp(db *sql.DB, groupID uint64) (GachaStepUpDesign, error) {
	rows, err := db.Query("SELECT id,ProtoBuf FROM GachaStepUpTable WHERE groupId=? ORDER BY id", groupID)
	if err != nil {
		return GachaStepUpDesign{}, err
	}
	defer rows.Close()
	out := GachaStepUpDesign{ID: groupID}
	for rows.Next() {
		var id uint64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return GachaStepUpDesign{}, err
		}
		one := func(field int) uint64 {
			values, _ := packedInts(raw, field)
			if len(values) == 1 {
				return values[0]
			}
			return 0
		}
		step := GachaStepDesign{Step: one(5), GroupID: one(2), GachaID: one(3), FixedID: one(1), IsDisplayFixedItem: one(6)}
		if id != step.Step || step.Step != uint64(len(out.Steps)+1) || step.GroupID == 0 || step.GachaID == 0 {
			return GachaStepUpDesign{}, fmt.Errorf("gamedata: malformed step-up group %d row %d", groupID, id)
		}
		var child []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM GachaGroupTable WHERE id=?", step.GroupID).Scan(&child); err != nil {
			return GachaStepUpDesign{}, fmt.Errorf("gamedata: step-up child group %d: %w", step.GroupID, err)
		}
		types, _ := packedInts(child, 17)
		subTypes, _ := packedInts(child, 16)
		tenIDs, _ := packedInts(child, 33)
		if len(types) != 1 || types[0] != 1 || len(subTypes) != 1 || subTypes[0] != 4 || len(tenIDs) != 1 || tenIDs[0] != step.GachaID {
			return GachaStepUpDesign{}, fmt.Errorf("gamedata: step-up group %d child %d does not reference gacha %d", groupID, step.GroupID, step.GachaID)
		}
		out.Steps = append(out.Steps, step)
	}
	if err := rows.Err(); err != nil {
		return GachaStepUpDesign{}, err
	}
	if len(out.Steps) == 0 {
		return GachaStepUpDesign{}, fmt.Errorf("gamedata: step-up group %d is empty", groupID)
	}
	return out, nil
}

func loadCostumeRewardGroup(db *sql.DB, id uint64, visiting map[uint64]bool) (*CostumeRewardGroup, error) {
	if visiting[id] {
		return nil, fmt.Errorf("reward group cycle at %d", id)
	}
	visiting[id] = true
	defer delete(visiting, id)
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM RewardGroupTable WHERE id=?", id).Scan(&raw); err != nil {
		return nil, err
	}
	one := func(field int) (uint64, bool) {
		values, _ := packedInts(raw, field)
		if len(values) == 1 {
			return values[0], true
		}
		return 0, false
	}
	dropCount, ok := one(1)
	if !ok || dropCount == 0 {
		return nil, errors.New("reward group has invalid drop count")
	}
	dropType, _ := one(2)
	ids, _ := packedInts(raw, 5)
	types, _ := packedInts(raw, 6)
	counts, _ := packedInts(raw, 4)
	weights, _ := packedInts(raw, 8)
	if len(ids) == 0 || len(ids) != len(types) || len(ids) != len(counts) || len(ids) != len(weights) {
		return nil, errors.New("malformed reward group")
	}
	group := &CostumeRewardGroup{ID: id, DropCount: dropCount, DropType: dropType, Entries: make([]CostumeRewardEntry, len(ids))}
	for i := range ids {
		entry := CostumeRewardEntry{ItemType: types[i], ItemID: ids[i], Count: counts[i], Weight: weights[i]}
		if entry.Count == 0 || entry.Weight == 0 {
			return nil, errors.New("reward group has zero count or weight")
		}
		switch entry.ItemType {
		case 9:
			child, err := loadCostumeRewardGroup(db, entry.ItemID, visiting)
			if err != nil {
				return nil, err
			}
			entry.Group = child
		case 11:
		default:
			return nil, fmt.Errorf("unsupported costume reward type %d", entry.ItemType)
		}
		group.Entries[i] = entry
	}
	return group, nil
}

func costumeRewardPool(group *CostumeRewardGroup) ([]WeightedCostume, bool) {
	if group == nil || group.DropType != 0 {
		return nil, false
	}
	out := make([]WeightedCostume, 0, len(group.Entries))
	for _, entry := range group.Entries {
		item := WeightedCostume{Weight: entry.Weight}
		if entry.Group != nil {
			if entry.Group.DropCount != 1 {
				return nil, false
			}
			children, ok := costumeRewardPool(entry.Group)
			if !ok {
				return nil, false
			}
			item.Children = children
		} else if entry.ItemType == 11 && entry.Count == 1 {
			item.ID = entry.ItemID
		} else {
			return nil, false
		}
		out = append(out, item)
	}
	return out, true
}

func fixedCostumeReward(group *CostumeRewardGroup) (uint64, []WeightedCostume) {
	if group == nil || group.DropType != 1 || len(group.Entries) != 2 {
		return 0, nil
	}
	var fixed uint64
	var remainder *CostumeRewardGroup
	for _, entry := range group.Entries {
		switch {
		case entry.Group == nil && entry.ItemType == 11 && entry.Count == 1:
			fixed = entry.ItemID
		case entry.Group != nil:
			remainder = entry.Group
		default:
			return 0, nil
		}
	}
	pool, ok := costumeRewardPool(remainder)
	if fixed == 0 || !ok {
		return 0, nil
	}
	return fixed, pool
}

func collectCostumeRewardIDs(group *CostumeRewardGroup, out *[]uint64) {
	if group == nil {
		return
	}
	for _, entry := range group.Entries {
		if entry.Group != nil {
			collectCostumeRewardIDs(entry.Group, out)
		} else if entry.ItemType == 11 {
			*out = append(*out, entry.ItemID)
		}
	}
}

func loadCostumeGrade(db *sql.DB, costumeID uint64) (uint64, error) {
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CharTable WHERE id=?", costumeID/10).Scan(&raw); err != nil {
		return 0, err
	}
	grades, _ := packedInts(raw, 9)
	if len(grades) != 1 || grades[0] < 3 || grades[0] > 5 {
		return 0, fmt.Errorf("gamedata: costume %d has invalid character grade %v", costumeID, grades)
	}
	return grades[0], nil
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
			ID: id, GachaType: readOne(17), BuyLimitCount: readOne(2), CashProductGroupID: readOne(3), CashProductID: readOne(4), CashSalesGroup: readOne(5), FixedID: readOne(10), PointCount: readOne(27), PickUpExchangeCost: readOne(25),
			PickUpCostumeID: readOne(26), OneTimeGachaID: readOne(24), TenTimeGachaID: readOne(33),
			SelectCount: readOne(29), SelectionChangeCount: readOne(30), SelectionChoiceRate: readOne(31), UseSelectionOnlyFixedApply: readOne(36) != 0,
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
		fixed, err := loadCostumeFixed(db, group.FixedID)
		if err != nil {
			return err
		}
		c.fixed[group.FixedID] = fixed
	}
	return nil
}

func loadCostumeFixed(db *sql.DB, id uint64) (GachaFixedDesign, error) {
	var proto []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM GachaFixedTable WHERE id=?", id).Scan(&proto); err != nil {
		return GachaFixedDesign{}, fmt.Errorf("gamedata: gacha fixed %d: %w", id, err)
	}
	readOptional := func(field int) (uint64, error) {
		values, err := packedInts(proto, field)
		if err != nil || len(values) > 1 {
			return 0, fmt.Errorf("gamedata: malformed costume gacha fixed %d field %d", id, field)
		}
		if len(values) == 1 {
			return values[0], nil
		}
		return 0, nil
	}
	four, err := readOptional(2)
	if err != nil {
		return GachaFixedDesign{}, err
	}
	five, err := readOptional(4)
	if err != nil {
		return GachaFixedDesign{}, err
	}
	reset, err := readOptional(6)
	if err != nil {
		return GachaFixedDesign{}, err
	}
	if four == 0 && five == 0 {
		return GachaFixedDesign{}, fmt.Errorf("gamedata: costume gacha fixed %d has no threshold", id)
	}
	return GachaFixedDesign{ID: id, CostumeGrade4Count: four, CostumeGrade5Count: five, ResetOnMatchingGrade: reset != 0}, nil
}

func (c *RegularGachaCatalog) loadStepFixed(db *sql.DB) error {
	for stepUpID, stepUp := range c.stepUps {
		for _, step := range stepUp.Steps {
			if _, ok := c.Gachas[step.GachaID]; !ok {
				return fmt.Errorf("gamedata: step-up group %d missing gacha %d", stepUpID, step.GachaID)
			}
			group, ok := c.groups[step.GroupID]
			if !ok || group.TenTimeGachaID != step.GachaID || group.GachaSubType != 4 {
				return fmt.Errorf("gamedata: step-up group %d has invalid child group %d", stepUpID, step.GroupID)
			}
			if step.FixedID == 0 {
				continue
			}
			group.FixedID = step.FixedID
			c.groups[step.GroupID] = group
			if _, ok := c.fixed[step.FixedID]; !ok {
				fixed, err := loadCostumeFixed(db, step.FixedID)
				if err != nil {
					return err
				}
				c.fixed[step.FixedID] = fixed
			}
		}
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
