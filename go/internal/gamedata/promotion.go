package gamedata

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// PromotionCost is one exact CharGrowthTable.ClassupItem{Type,Id,Count} row.
// Currency (type 4) has no inventory index; the other rows are owned items.
type PromotionCost struct{ Type, ID, Count uint64 }

// PromotionGrowthResult is the authoritative result of one CharGrowth request
// that can cross one or more class-up boundaries. Costs contains the exact
// cumulative class-up costs selected by the request's aggregate gold amount.
type PromotionGrowthResult struct {
	CharacterID uint64
	Level       uint64
	Exp         uint64
	Costs       []PromotionCost
	Refunds     []GrowthMaterial
}

// CharacterPromotion reads the installed character and growth tables. At a
// stage cap the client sends its class-up materials through CharGrowth, then
// detects a promotion by comparing the response character ID to the old ID.
func CharacterPromotion(root, version string, charID int, level, exp uint64) (uint64, []PromotionCost, error) {
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return 0, nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-promotion-")
	if err != nil {
		return 0, nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "common.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		return 0, nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return 0, nil, err
	}
	defer db.Close()
	return characterPromotion(db, charID, level, exp)
}

// CharacterGrowthPromotions applies a client's combined class-up/growth
// request. The UI aggregates gold and class-up resources for every boundary
// crossed by the selected target level, while ordinary EXP resources remain
// in the same request. Gold therefore identifies an exact prefix of the
// CharTable.NextCharId chain; each prefix cost is then subtracted from the
// submitted resources and only the remainder contributes EXP.
func CharacterGrowthPromotions(root, version string, charID int, level, exp uint64, submitted []PromotionCost) (PromotionGrowthResult, error) {
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return PromotionGrowthResult{}, err
	}
	dir, err := os.MkdirTemp("", "bd2-promotion-growth-")
	if err != nil {
		return PromotionGrowthResult{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "common.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		return PromotionGrowthResult{}, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return PromotionGrowthResult{}, err
	}
	defer db.Close()
	return characterGrowthPromotions(db, charID, level, exp, submitted)
}

type promotionStage struct {
	fromID, nextID uint64
	maximum        uint64
	costs          []PromotionCost
}

func characterGrowthPromotions(db *sql.DB, charID int, level, exp uint64, submitted []PromotionCost) (PromotionGrowthResult, error) {
	if charID <= 0 || level == 0 || len(submitted) == 0 {
		return PromotionGrowthResult{}, fmt.Errorf("gamedata: invalid combined promotion input")
	}
	requested := make(map[[2]uint64]uint64, len(submitted))
	var requestedGold uint64
	for _, cost := range submitted {
		if cost.Count == 0 || (cost.Type != 4 && (cost.Type != 8 || cost.ID == 0)) {
			return PromotionGrowthResult{}, fmt.Errorf("gamedata: invalid submitted promotion material")
		}
		key := [2]uint64{cost.Type, cost.ID}
		if cost.Count > ^uint64(0)-requested[key] {
			return PromotionGrowthResult{}, fmt.Errorf("gamedata: submitted promotion material overflow")
		}
		requested[key] += cost.Count
		if cost.Type == 4 {
			if cost.ID != 0 || requestedGold != 0 {
				return PromotionGrowthResult{}, fmt.Errorf("gamedata: invalid submitted promotion currency")
			}
			requestedGold = cost.Count
		}
	}
	if requestedGold == 0 {
		return PromotionGrowthResult{}, fmt.Errorf("gamedata: combined promotion has no gold")
	}

	currentID := uint64(charID)
	var stages []promotionStage
	var cumulative []PromotionCost
	var cumulativeGold uint64
	for len(stages) < 32 {
		nextID, costs, maximum, err := promotionDefinition(db, currentID)
		if err != nil {
			return PromotionGrowthResult{}, err
		}
		stages = append(stages, promotionStage{fromID: currentID, nextID: nextID, maximum: maximum, costs: costs})
		cumulative = append(cumulative, costs...)
		for _, cost := range costs {
			if cost.Type == 4 {
				if cost.Count > ^uint64(0)-cumulativeGold {
					return PromotionGrowthResult{}, fmt.Errorf("gamedata: cumulative promotion gold overflow")
				}
				cumulativeGold += cost.Count
			}
		}
		if cumulativeGold == requestedGold {
			break
		}
		if cumulativeGold > requestedGold {
			return PromotionGrowthResult{}, fmt.Errorf("gamedata: submitted gold %d does not match a promotion prefix", requestedGold)
		}
		currentID = nextID
	}
	if cumulativeGold != requestedGold {
		return PromotionGrowthResult{}, fmt.Errorf("gamedata: submitted gold %d exceeds the promotion chain", requestedGold)
	}

	remaining := make(map[[2]uint64]uint64, len(requested))
	for key, count := range requested {
		remaining[key] = count
	}
	for _, cost := range cumulative {
		key := [2]uint64{cost.Type, cost.ID}
		if remaining[key] < cost.Count {
			return PromotionGrowthResult{}, fmt.Errorf("gamedata: missing cumulative promotion cost %d/%d x%d", cost.Type, cost.ID, cost.Count-remaining[key])
		}
		remaining[key] -= cost.Count
	}
	if remaining[[2]uint64{4, 0}] != 0 {
		return PromotionGrowthResult{}, fmt.Errorf("gamedata: unexpected promotion gold remainder")
	}

	var gained uint64
	for key, count := range remaining {
		if count == 0 {
			continue
		}
		if key[0] != 8 || key[1] == 0 {
			return PromotionGrowthResult{}, fmt.Errorf("gamedata: unexpected combined promotion material %d/%d x%d", key[0], key[1], count)
		}
		var proto []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM ResourceTable WHERE id=?", key[1]).Scan(&proto); err != nil {
			return PromotionGrowthResult{}, fmt.Errorf("gamedata: promotion growth resource %d: %w", key[1], err)
		}
		values, err := packedInts(proto, 9)
		if err != nil || len(values) != 1 || values[0] == 0 {
			return PromotionGrowthResult{}, fmt.Errorf("gamedata: resource %d is not an experience material", key[1])
		}
		if count > (^uint64(0)-gained)/values[0] {
			return PromotionGrowthResult{}, fmt.Errorf("gamedata: combined promotion experience overflow")
		}
		gained += count * values[0]
	}

	currentID = uint64(charID)
	currentLevel := level
	currentExp := exp
	for _, stage := range stages {
		if currentID != stage.fromID {
			return PromotionGrowthResult{}, fmt.Errorf("gamedata: invalid promotion stage sequence")
		}
		definition, err := growthDefinition(db, currentID)
		if err != nil {
			return PromotionGrowthResult{}, err
		}
		currentLevel, currentExp, gained, err = applyGrowthExperience(db, definition, currentLevel, currentExp, gained)
		if err != nil {
			return PromotionGrowthResult{}, err
		}
		if currentLevel != stage.maximum {
			return PromotionGrowthResult{}, fmt.Errorf("gamedata: insufficient experience to reach promotion cap %d", definition.maximum)
		}
		currentID = stage.nextID
	}
	finalDefinition, err := growthDefinition(db, currentID)
	if err != nil {
		return PromotionGrowthResult{}, err
	}
	currentLevel, currentExp, gained, err = applyGrowthExperience(db, finalDefinition, currentLevel, currentExp, gained)
	if err != nil {
		return PromotionGrowthResult{}, err
	}
	refunds, err := refundGrowthResources(db, gained)
	if err != nil {
		return PromotionGrowthResult{}, err
	}
	return PromotionGrowthResult{CharacterID: currentID, Level: currentLevel, Exp: currentExp, Costs: cumulative, Refunds: refunds}, nil
}

type characterGrowthDefinition struct {
	group, maximum uint64
}

func growthDefinition(db *sql.DB, charID uint64) (characterGrowthDefinition, error) {
	var charProto []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CharTable WHERE id=?", charID).Scan(&charProto); err != nil {
		return characterGrowthDefinition{}, fmt.Errorf("gamedata: character %d: %w", charID, err)
	}
	growthIDs, _ := packedInts(charProto, 1)
	if len(growthIDs) != 1 {
		return characterGrowthDefinition{}, fmt.Errorf("gamedata: character %d has no growth definition", charID)
	}
	var growthProto []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CharGrowthTable WHERE id=?", growthIDs[0]).Scan(&growthProto); err != nil {
		return characterGrowthDefinition{}, err
	}
	groups, _ := packedInts(growthProto, 1)
	maximum, _ := packedInts(growthProto, 9)
	if len(groups) != 1 || len(maximum) != 1 {
		return characterGrowthDefinition{}, fmt.Errorf("gamedata: invalid growth definition %d", growthIDs[0])
	}
	return characterGrowthDefinition{group: groups[0], maximum: maximum[0]}, nil
}

func promotionDefinition(db *sql.DB, charID uint64) (uint64, []PromotionCost, uint64, error) {
	definition, err := growthDefinition(db, charID)
	if err != nil {
		return 0, nil, 0, err
	}
	var charProto []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CharTable WHERE id=?", charID).Scan(&charProto); err != nil {
		return 0, nil, 0, err
	}
	next, _ := packedInts(charProto, 15)
	if len(next) != 1 || next[0] == 0 {
		return 0, nil, 0, fmt.Errorf("gamedata: character %d has no next stage", charID)
	}
	growthIDs, _ := packedInts(charProto, 1)
	var growthProto []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CharGrowthTable WHERE id=?", growthIDs[0]).Scan(&growthProto); err != nil {
		return 0, nil, 0, err
	}
	types, _ := packedInts(growthProto, 4)
	ids, _ := packedInts(growthProto, 3)
	counts, _ := packedInts(growthProto, 2)
	if len(types) == 0 || len(types) != len(ids) || len(types) != len(counts) {
		return 0, nil, 0, fmt.Errorf("gamedata: invalid promotion costs for character %d", charID)
	}
	costs := make([]PromotionCost, len(types))
	for i := range costs {
		costs[i] = PromotionCost{Type: types[i], ID: ids[i], Count: counts[i]}
	}
	return next[0], costs, definition.maximum, nil
}

func applyGrowthExperience(db *sql.DB, definition characterGrowthDefinition, level, exp, gained uint64) (uint64, uint64, uint64, error) {
	if level > definition.maximum {
		return 0, 0, 0, fmt.Errorf("gamedata: level %d exceeds growth maximum %d", level, definition.maximum)
	}
	for gained > 0 && level < definition.maximum {
		var proto []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM CharLevelTable WHERE GroupId=? AND id=?", definition.group, level).Scan(&proto); err != nil {
			return 0, 0, 0, err
		}
		required, _ := packedInts(proto, 8)
		if len(required) != 1 || required[0] == 0 || exp >= required[0] {
			return 0, 0, 0, fmt.Errorf("gamedata: invalid level curve group %d level %d", definition.group, level)
		}
		need := required[0] - exp
		if gained < need {
			exp += gained
			gained = 0
			break
		}
		gained -= need
		level++
		exp = 0
	}
	if level == definition.maximum {
		exp = 0
	}
	return level, exp, gained, nil
}

func characterPromotion(db *sql.DB, charID int, level, exp uint64) (uint64, []PromotionCost, error) {
	if charID <= 0 || level == 0 {
		return 0, nil, fmt.Errorf("gamedata: invalid promotion character or level")
	}
	var current []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CharTable WHERE id=?", charID).Scan(&current); err != nil {
		return 0, nil, fmt.Errorf("gamedata: promotion character %d: %w", charID, err)
	}
	next, err := packedInts(current, 15)
	if err != nil || len(next) != 1 || next[0] == 0 {
		return 0, nil, fmt.Errorf("gamedata: character %d has no next stage", charID)
	}
	growthID, err := packedInts(current, 1)
	if err != nil || len(growthID) != 1 {
		return 0, nil, fmt.Errorf("gamedata: character %d has no growth definition", charID)
	}
	var growth []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CharGrowthTable WHERE id=?", growthID[0]).Scan(&growth); err != nil {
		return 0, nil, err
	}
	maximum, _ := packedInts(growth, 9)
	group, _ := packedInts(growth, 1)
	if len(maximum) != 1 || len(group) != 1 || level != maximum[0] {
		return 0, nil, fmt.Errorf("gamedata: character %d has not reached its promotion level", charID)
	}
	var curve []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CharLevelTable WHERE GroupId=? AND id=?", group[0], level).Scan(&curve); err != nil {
		return 0, nil, err
	}
	required, _ := packedInts(curve, 8)
	if len(required) != 0 && exp < required[0] {
		return 0, nil, fmt.Errorf("gamedata: character %d has not reached its promotion experience", charID)
	}
	var nextProto []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CharTable WHERE id=?", next[0]).Scan(&nextProto); err != nil {
		return 0, nil, fmt.Errorf("gamedata: next character %d: %w", next[0], err)
	}
	nextGrowth, _ := packedInts(nextProto, 1)
	if len(nextGrowth) != 1 || nextGrowth[0] == growthID[0] {
		return 0, nil, fmt.Errorf("gamedata: invalid promotion %d -> %d", charID, next[0])
	}
	types, _ := packedInts(growth, 4)
	ids, _ := packedInts(growth, 3)
	counts, _ := packedInts(growth, 2)
	if len(types) == 0 || len(types) != len(ids) || len(types) != len(counts) {
		return 0, nil, fmt.Errorf("gamedata: invalid promotion costs for character %d", charID)
	}
	costs := make([]PromotionCost, len(types))
	for i := range costs {
		if counts[i] == 0 || (types[i] != 4 && ids[i] == 0) {
			return 0, nil, fmt.Errorf("gamedata: invalid promotion cost for character %d", charID)
		}
		costs[i] = PromotionCost{Type: types[i], ID: ids[i], Count: counts[i]}
	}
	return next[0], costs, nil
}
