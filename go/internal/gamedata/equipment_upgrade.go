package gamedata

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

type EquipmentUpgradeLevel struct {
	Level, GrowthPoint uint64
	Costs              []PromotionCost
	SuccessRatio       float64
}

type EquipmentUpgradeDesign struct {
	MaxLevel  map[uint64]uint64
	Group     map[uint64]uint64
	RankGroup map[uint64]uint64
	Levels    map[[2]uint64]EquipmentUpgradeLevel
	RankRatio map[[2]uint64][]float64
	Break     map[[2]uint64][]BattleReward
	NotTrash  map[uint64]bool
	roll      func(float64) (bool, error)
	rankRoll  func([]float64) (uint64, error)
}

func LoadEquipmentUpgradeDesign(root, version string) (*EquipmentUpgradeDesign, error) {
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-equipment-upgrade-")
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
	return loadEquipmentUpgradeDesign(db)
}

func loadEquipmentUpgradeDesign(db *sql.DB) (*EquipmentUpgradeDesign, error) {
	d := &EquipmentUpgradeDesign{
		MaxLevel: map[uint64]uint64{}, Group: map[uint64]uint64{}, RankGroup: map[uint64]uint64{},
		Levels: map[[2]uint64]EquipmentUpgradeLevel{}, RankRatio: map[[2]uint64][]float64{},
		Break: map[[2]uint64][]BattleReward{}, NotTrash: map[uint64]bool{},
		roll: cryptoRatioRoll, rankRoll: cryptoRankRoll,
	}
	groupMaximum := map[uint64]uint64{}
	rankGroups := map[uint64]bool{}
	rows, err := db.Query("SELECT id,ProtoBuf FROM EquipmentTable")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint64
		var proto []byte
		if err := rows.Scan(&id, &proto); err != nil {
			rows.Close()
			return nil, err
		}
		groups, _ := packedInts(proto, 4)
		maximum, _ := packedInts(proto, 13)
		notTrash, _ := packedInts(proto, 14)
		rankGroup, _ := packedInts(proto, 19)
		if len(groups) != 1 || len(maximum) != 1 || len(rankGroup) != 1 || maximum[0] == 0 || rankGroup[0] == 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment %d invalid upgrade design", id)
		}
		d.Group[id], d.MaxLevel[id], d.RankGroup[id] = groups[0], maximum[0], rankGroup[0]
		d.NotTrash[id] = len(notTrash) == 1 && notTrash[0] != 0
		rankGroups[rankGroup[0]] = true
		if prior, found := groupMaximum[groups[0]]; found && prior != maximum[0] {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment growth group %d has inconsistent maxima", groups[0])
		}
		groupMaximum[groups[0]] = maximum[0]
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT groupId,id,ProtoBuf FROM EquipmentGrowthTable")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var group, level uint64
		var proto []byte
		if err := rows.Scan(&group, &level, &proto); err != nil {
			rows.Close()
			return nil, err
		}
		maximum, referenced := groupMaximum[group]
		if !referenced {
			continue
		}
		breakCounts, _ := packedInts(proto, 1)
		breakIDs, _ := packedInts(proto, 2)
		breakTypes, _ := packedInts(proto, 3)
		if len(breakCounts) == 0 || len(breakCounts) != len(breakIDs) || len(breakCounts) != len(breakTypes) {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment break result %d/%d malformed", group, level)
		}
		breakRewards := make([]BattleReward, 0, len(breakCounts))
		for i := range breakCounts {
			if breakCounts[i] == 0 || breakIDs[i] == 0 || breakTypes[i] == 0 {
				rows.Close()
				return nil, fmt.Errorf("gamedata: equipment break result %d/%d invalid", group, level)
			}
			breakRewards = append(breakRewards, BattleReward{Type: breakTypes[i], ID: breakIDs[i], Count: breakCounts[i]})
		}
		d.Break[[2]uint64{group, level}] = breakRewards
		// EquipmentGrowthTable includes the terminal maximum-level row, which
		// intentionally has no next-upgrade cost or success ratio.
		if level >= maximum {
			continue
		}
		counts, _ := packedInts(proto, 7)
		ids, _ := packedInts(proto, 8)
		types, _ := packedInts(proto, 9)
		point, _ := packedInts(proto, 5)
		ratio, present, err := fixed64Double(proto, 10)
		if err != nil || !present || ratio < 0 || ratio > 1 || len(point) != 1 || len(counts) == 0 || len(counts) != len(ids) || len(counts) != len(types) {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment growth %d/%d malformed", group, level)
		}
		entry := EquipmentUpgradeLevel{Level: level, GrowthPoint: point[0], SuccessRatio: ratio}
		for i := range counts {
			if counts[i] == 0 || (types[i] != 4 && types[i] != 8) || (types[i] == 4 && ids[i] != 0) || (types[i] == 8 && ids[i] == 0) {
				rows.Close()
				return nil, fmt.Errorf("gamedata: equipment growth %d/%d invalid cost", group, level)
			}
			entry.Costs = append(entry.Costs, PromotionCost{Type: types[i], ID: ids[i], Count: counts[i]})
		}
		d.Levels[[2]uint64{group, level}] = entry
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT groupId,id,ProtoBuf FROM EquipmentRankTable")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var group, slot uint64
		var proto []byte
		if err := rows.Scan(&group, &slot, &proto); err != nil {
			rows.Close()
			return nil, err
		}
		if !rankGroups[group] {
			continue
		}
		ratios, err := fixed32Floats(proto, 4)
		if err != nil || slot < 1 || slot > 3 || len(ratios) != 4 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment rank %d/%d malformed", group, slot)
		}
		var total float64
		for _, ratio := range ratios {
			if ratio < 0 || ratio > 1 || math.IsNaN(ratio) {
				rows.Close()
				return nil, fmt.Errorf("gamedata: equipment rank %d/%d invalid ratio", group, slot)
			}
			total += ratio
		}
		if math.Abs(total-1) > 1e-5 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment rank %d/%d ratio total %.8f", group, slot, total)
		}
		d.RankRatio[[2]uint64{group, slot}] = append([]float64(nil), ratios...)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for group := range rankGroups {
		for slot := uint64(1); slot <= 3; slot++ {
			if len(d.RankRatio[[2]uint64{group, slot}]) != 4 {
				return nil, fmt.Errorf("gamedata: missing equipment rank %d/%d", group, slot)
			}
		}
	}
	return d, nil
}

func (d *EquipmentUpgradeDesign) CanBreak(equipmentID uint64) bool {
	if d == nil || equipmentID == 0 {
		return false
	}
	// EquipmentTable.NotTrash controls the ordinary discard button. The
	// client's batch break predicate is instead EquipDBInfo.IsAvailableBreak
	// (not equipped and not locked), so crafting results with NotTrash=1 are
	// still valid inputs to EquipMakingToBreakAuto.
	_, exists := d.Group[equipmentID]
	return exists
}

func (d *EquipmentUpgradeDesign) BreakRewards(equipmentID, level uint64) ([]BattleReward, error) {
	if !d.CanBreak(equipmentID) {
		return nil, fmt.Errorf("gamedata: equipment %d cannot be broken", equipmentID)
	}
	maximum := d.MaxLevel[equipmentID]
	if level > maximum {
		return nil, fmt.Errorf("gamedata: equipment %d invalid break level %d", equipmentID, level)
	}
	rewards, exists := d.Break[[2]uint64{d.Group[equipmentID], level}]
	if !exists || len(rewards) == 0 {
		return nil, fmt.Errorf("gamedata: missing equipment break result %d/%d", d.Group[equipmentID], level)
	}
	return append([]BattleReward(nil), rewards...), nil
}

func (d *EquipmentUpgradeDesign) Level(equipmentID, level uint64) (EquipmentUpgradeLevel, uint64, error) {
	if d == nil || equipmentID == 0 {
		return EquipmentUpgradeLevel{}, 0, fmt.Errorf("gamedata: invalid equipment upgrade request")
	}
	maximum, exists := d.MaxLevel[equipmentID]
	if !exists {
		return EquipmentUpgradeLevel{}, 0, fmt.Errorf("gamedata: unknown equipment %d", equipmentID)
	}
	if level >= maximum {
		return EquipmentUpgradeLevel{}, maximum, fmt.Errorf("gamedata: equipment %d already at maximum level %d", equipmentID, maximum)
	}
	entry, exists := d.Levels[[2]uint64{d.Group[equipmentID], level}]
	if !exists {
		return EquipmentUpgradeLevel{}, maximum, fmt.Errorf("gamedata: missing equipment growth %d/%d", d.Group[equipmentID], level)
	}
	return entry, maximum, nil
}

func (d *EquipmentUpgradeDesign) Roll(ratio float64) (bool, error) {
	if d == nil {
		return false, fmt.Errorf("gamedata: equipment upgrade roller unavailable")
	}
	if d.roll == nil {
		return cryptoRatioRoll(ratio)
	}
	return d.roll(ratio)
}

// RollRank returns the official C/B/A/S grade number (1..4) for the slot
// unlocked at +3, +6 or +9. The distributions come from EquipmentRankTable.
func (d *EquipmentUpgradeDesign) RollRank(equipmentID, slot uint64) (uint64, error) {
	if d == nil || slot < 1 || slot > 3 {
		return 0, fmt.Errorf("gamedata: invalid equipment rank request")
	}
	group, exists := d.RankGroup[equipmentID]
	if !exists {
		return 0, fmt.Errorf("gamedata: unknown equipment rank design %d", equipmentID)
	}
	ratios := d.RankRatio[[2]uint64{group, slot}]
	if len(ratios) != 4 {
		return 0, fmt.Errorf("gamedata: missing equipment rank %d/%d", group, slot)
	}
	if d.rankRoll != nil {
		return d.rankRoll(ratios)
	}
	return cryptoRankRoll(ratios)
}

func cryptoRatioRoll(ratio float64) (bool, error) {
	if ratio <= 0 {
		return false, nil
	}
	if ratio >= 1 {
		return true, nil
	}
	value, err := cryptoUnitFloat()
	return value < ratio && !math.IsNaN(ratio), err
}

func cryptoRankRoll(ratios []float64) (uint64, error) {
	if len(ratios) != 4 {
		return 0, fmt.Errorf("gamedata: invalid equipment rank ratios")
	}
	value, err := cryptoUnitFloat()
	if err != nil {
		return 0, err
	}
	var cumulative float64
	for i, ratio := range ratios {
		cumulative += ratio
		if value < cumulative || i == len(ratios)-1 {
			return uint64(i + 1), nil
		}
	}
	return 0, fmt.Errorf("gamedata: equipment rank roll failed")
}

func cryptoUnitFloat() (float64, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, err
	}
	value := binary.LittleEndian.Uint64(raw[:]) >> 11
	return float64(value) / float64(uint64(1)<<53), nil
}
