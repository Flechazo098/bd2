package gamedata

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// EquipmentSmeltingDesign contains only static 2.34.13 GameData facts. It does
// not decide whether a lower-score result is applied or how mileage is paid:
// those are server transactions and require an official response capture.
type EquipmentSmeltingDesign struct {
	Equipment map[uint64]EquipmentSmeltingItem
	Ranks     map[[2]uint64]EquipmentSmeltingRank
	Grades    map[uint64][]PromotionCost
	Mileage   EquipmentSmeltingMileage
	MaxStreak uint64
}

type EquipmentSmeltingItem struct {
	Grade, RankGroup, MaxLevel uint64
}

type EquipmentSmeltingRank struct {
	// GrowthPoint is the per-rank battle-power contribution. Values is the
	// separate 1..4 score used to compare a refinement candidate.
	GrowthPoint []uint64
	Values      []uint64
	Ratio       []float64
}

type EquipmentSmeltingMileage struct {
	UseType, UseID, UseCount          uint64
	RewardType, RewardID, RewardCount uint64
}

func LoadEquipmentSmeltingDesign(root, version string) (*EquipmentSmeltingDesign, error) {
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-equipment-smelting-")
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
	return loadEquipmentSmeltingDesign(db)
}

func loadEquipmentSmeltingDesign(db *sql.DB) (*EquipmentSmeltingDesign, error) {
	d := &EquipmentSmeltingDesign{
		Equipment: make(map[uint64]EquipmentSmeltingItem),
		Ranks:     make(map[[2]uint64]EquipmentSmeltingRank),
		Grades:    make(map[uint64][]PromotionCost),
	}
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
		grade, _ := packedInts(proto, 3)
		maximum, _ := packedInts(proto, 13)
		rankGroup, _ := packedInts(proto, 19)
		if len(grade) != 1 || len(maximum) != 1 || len(rankGroup) != 1 || grade[0] == 0 || maximum[0] == 0 || rankGroup[0] == 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment %d has invalid smelting design", id)
		}
		d.Equipment[id] = EquipmentSmeltingItem{Grade: grade[0], RankGroup: rankGroup[0], MaxLevel: maximum[0]}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT id,ProtoBuf FROM EquipmentGradeTable")
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
		counts, _ := packedInts(proto, 4)
		ids, _ := packedInts(proto, 5)
		types, _ := packedInts(proto, 6)
		if len(counts) == 0 || len(counts) != len(ids) || len(counts) != len(types) {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment grade %d has invalid smelting costs", id)
		}
		for i := range counts {
			if counts[i] == 0 || (types[i] != 4 && types[i] != 8) || (types[i] == 4 && ids[i] != 0) || (types[i] == 8 && ids[i] == 0) {
				rows.Close()
				return nil, fmt.Errorf("gamedata: equipment grade %d has invalid smelting material", id)
			}
			d.Grades[id] = append(d.Grades[id], PromotionCost{Type: types[i], ID: ids[i], Count: counts[i]})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
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
		ratios, err := fixed32Floats(proto, 4)
		growth, growthErr := packedInts(proto, 2)
		values, valueErr := packedInts(proto, 5)
		if err != nil || growthErr != nil || valueErr != nil || slot < 1 || slot > 3 || len(ratios) != 4 || len(growth) != 4 || len(values) != 4 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment rank %d/%d has invalid smelting distribution", group, slot)
		}
		var total float64
		for i := range ratios {
			if ratios[i] < 0 || ratios[i] > 1 || math.IsNaN(ratios[i]) || values[i] == 0 || growth[i] == 0 {
				rows.Close()
				return nil, fmt.Errorf("gamedata: equipment rank %d/%d has invalid smelting value", group, slot)
			}
			total += ratios[i]
			if i != 0 && (values[i] <= values[i-1] || growth[i] <= growth[i-1]) {
				rows.Close()
				return nil, fmt.Errorf("gamedata: equipment rank %d/%d is not increasing", group, slot)
			}
		}
		if math.Abs(total-1) > 1e-5 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment rank %d/%d ratio total %.8f", group, slot, total)
		}
		d.Ranks[[2]uint64{group, slot}] = EquipmentSmeltingRank{GrowthPoint: growth, Values: values, Ratio: ratios}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for id, equipment := range d.Equipment {
		if len(d.Grades[equipment.Grade]) == 0 {
			return nil, fmt.Errorf("gamedata: equipment %d has no grade costs", id)
		}
		for slot := uint64(1); slot <= 3; slot++ {
			if len(d.Ranks[[2]uint64{equipment.RankGroup, slot}].Values) != 4 {
				return nil, fmt.Errorf("gamedata: equipment %d has no rank slot %d", id, slot)
			}
		}
	}
	var mileageProto []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM EquipmentMileageTable WHERE id=0").Scan(&mileageProto); err != nil {
		return nil, fmt.Errorf("gamedata: equipment mileage: %w", err)
	}
	mileageCount, _ := packedInts(mileageProto, 2)
	mileageID, _ := packedInts(mileageProto, 3)
	mileageType, _ := packedInts(mileageProto, 4)
	useCount, _ := packedInts(mileageProto, 5)
	useID, _ := packedInts(mileageProto, 6)
	useType, _ := packedInts(mileageProto, 7)
	if len(mileageCount) != 1 || len(mileageID) > 1 || len(mileageType) != 1 || len(useCount) != 1 || len(useID) != 1 || len(useType) != 1 || mileageCount[0] == 0 || useCount[0] == 0 {
		return nil, fmt.Errorf("gamedata: invalid equipment mileage definition")
	}
	if len(mileageID) == 1 {
		d.Mileage.RewardID = mileageID[0]
	}
	d.Mileage.RewardCount, d.Mileage.RewardType = mileageCount[0], mileageType[0]
	d.Mileage.UseCount, d.Mileage.UseID, d.Mileage.UseType = useCount[0], useID[0], useType[0]
	if d.Mileage.RewardType == 0 || d.Mileage.UseType == 0 || d.Mileage.UseID == 0 {
		return nil, fmt.Errorf("gamedata: invalid equipment mileage item types")
	}
	for grade, costs := range d.Grades {
		found := false
		for _, cost := range costs {
			if cost.Type == d.Mileage.UseType && cost.ID == d.Mileage.UseID {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("gamedata: equipment grade %d does not consume mileage material", grade)
		}
	}
	var defaults []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM GameDefaultTable WHERE id=0").Scan(&defaults); err != nil {
		return nil, fmt.Errorf("gamedata: equipment smelting limit: %w", err)
	}
	maxStreak, _ := packedInts(defaults, 5)
	if len(maxStreak) != 1 || maxStreak[0] == 0 {
		return nil, fmt.Errorf("gamedata: invalid maximum smelting streak")
	}
	d.MaxStreak = maxStreak[0]
	return d, nil
}

func (d *EquipmentSmeltingDesign) Cost(equipmentID uint64) ([]PromotionCost, error) {
	if d == nil {
		return nil, fmt.Errorf("gamedata: equipment smelting design unavailable")
	}
	equipment, ok := d.Equipment[equipmentID]
	if !ok || len(d.Grades[equipment.Grade]) == 0 {
		return nil, fmt.Errorf("gamedata: equipment %d has no smelting cost", equipmentID)
	}
	return append([]PromotionCost(nil), d.Grades[equipment.Grade]...), nil
}

func (d *EquipmentSmeltingDesign) Score(equipmentID uint64, ranks []uint64) (uint64, error) {
	if d == nil || len(ranks) != 3 {
		return 0, fmt.Errorf("gamedata: invalid equipment rank score input")
	}
	equipment, ok := d.Equipment[equipmentID]
	if !ok {
		return 0, fmt.Errorf("gamedata: equipment %d has no smelting design", equipmentID)
	}
	var score uint64
	for i, rank := range ranks {
		values := d.Ranks[[2]uint64{equipment.RankGroup, uint64(i + 1)}].Values
		if rank < 1 || rank > uint64(len(values)) || values[rank-1] > ^uint64(0)-score {
			return 0, fmt.Errorf("gamedata: invalid equipment rank %d in slot %d", rank, i+1)
		}
		score += values[rank-1]
	}
	return score, nil
}

func (d *EquipmentSmeltingDesign) MaximumRanks(equipmentID uint64) ([]uint64, error) {
	if d == nil {
		return nil, fmt.Errorf("gamedata: equipment smelting design unavailable")
	}
	equipment, ok := d.Equipment[equipmentID]
	if !ok {
		return nil, fmt.Errorf("gamedata: equipment %d has no smelting design", equipmentID)
	}
	ranks := make([]uint64, 3)
	for slot := uint64(1); slot <= 3; slot++ {
		values := d.Ranks[[2]uint64{equipment.RankGroup, slot}].Values
		if len(values) == 0 {
			return nil, fmt.Errorf("gamedata: equipment %d has no rank slot %d", equipmentID, slot)
		}
		ranks[slot-1] = uint64(len(values))
	}
	return ranks, nil
}

// RollCandidate independently rolls all three rank slots from the official
// EquipmentRankTable distributions. Whether the candidate replaces the
// current ranks is a player-state transaction, not a GameData concern.
func (d *EquipmentSmeltingDesign) RollCandidate(equipmentID uint64) ([]uint64, error) {
	if d == nil {
		return nil, fmt.Errorf("gamedata: equipment smelting design unavailable")
	}
	equipment, ok := d.Equipment[equipmentID]
	if !ok {
		return nil, fmt.Errorf("gamedata: equipment %d has no smelting design", equipmentID)
	}
	result := make([]uint64, 3)
	for slot := uint64(1); slot <= 3; slot++ {
		rank := d.Ranks[[2]uint64{equipment.RankGroup, slot}]
		value, err := cryptoRankRoll(rank.Ratio)
		if err != nil {
			return nil, fmt.Errorf("gamedata: roll equipment %d rank slot %d: %w", equipmentID, slot, err)
		}
		result[slot-1] = value
	}
	return result, nil
}
