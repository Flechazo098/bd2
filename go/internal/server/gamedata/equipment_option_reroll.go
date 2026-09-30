package gamedata

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// EquipmentOptionRerollDesign is the static equipment-option refinement
// program from GameData. It deliberately does not impose cross-slot
// uniqueness or reject the previous option: neither rule is present in the
// design tables.
type EquipmentOptionRerollDesign struct {
	Equipment  map[uint64]EquipmentOptionRerollItem
	Costs      map[uint64]EquipmentOptionRerollCost
	Groups     map[uint64]OptionGroup
	Conversion *EquipmentOptionRerollConversion
	draw       func(uint64) (uint64, error)
}

// EquipmentOptionRerollConversion describes the optional material
// substitution shown by the client. Ratio Source resources have the value of
// one Target resource; requests still carry the concrete stacks consumed.
type EquipmentOptionRerollConversion struct {
	Ratio                uint64
	SourceType, SourceID uint64
	TargetType, TargetID uint64
}

type EquipmentOptionRerollItem struct {
	ID, OptionRerollID, PrivateUniqueCharID uint64
	MainGroups                              []uint64
	SubGroups                               []uint64
	PrivateGroups                           []uint64
}

// EquipmentOptionRerollCost holds parallel GameData resource arrays. A
// resource costs BaseCount + LockCount*lockedSlots for one refinement.
type EquipmentOptionRerollCost struct {
	Resources []EquipmentOptionRerollResource
}

type EquipmentOptionRerollResource struct {
	Type, ID, BaseCount, LockCount uint64
}

type EquipmentOptionRerollLocks struct {
	Main, Sub, Private []bool
}

type EquipmentOptionRerollRoll struct {
	// Locked entries are left as the zero value. Callers preserve the
	// corresponding current option when applying the candidate.
	Main, Sub, Private []EquipmentOptionChoice
}

func LoadEquipmentOptionRerollDesign(root, version string) (*EquipmentOptionRerollDesign, error) {
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-equipment-option-reroll-")
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
	return loadEquipmentOptionRerollDesign(db)
}

func loadEquipmentOptionRerollDesign(db *sql.DB) (*EquipmentOptionRerollDesign, error) {
	d := &EquipmentOptionRerollDesign{
		Equipment: make(map[uint64]EquipmentOptionRerollItem),
		Costs:     make(map[uint64]EquipmentOptionRerollCost),
		Groups:    make(map[uint64]OptionGroup),
		draw:      cryptoDraw,
	}
	referencedGroups := make(map[uint64]bool)
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
		protoID, idErr := packedInts(proto, 6)
		rerollID, rerollErr := packedInts(proto, 15)
		main, mainErr := packedInts(proto, 12)
		sub, subErr := packedInts(proto, 21)
		private, privateErr := packedInts(proto, 17)
		privateUniqueCharID, uniqueErr := packedInts(proto, 16)
		if idErr != nil || rerollErr != nil || mainErr != nil || subErr != nil || privateErr != nil || uniqueErr != nil ||
			len(protoID) != 1 || protoID[0] != id || len(rerollID) != 1 || rerollID[0] == 0 || len(main) == 0 || len(sub) == 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment %d has invalid option-reroll design", id)
		}
		item := EquipmentOptionRerollItem{
			ID:             id,
			OptionRerollID: rerollID[0],
			MainGroups:     append([]uint64(nil), main...),
			SubGroups:      append([]uint64(nil), sub...),
			PrivateGroups:  append([]uint64(nil), private...),
		}
		if len(privateUniqueCharID) > 1 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment %d has invalid private unique character", id)
		}
		if len(privateUniqueCharID) == 1 {
			item.PrivateUniqueCharID = privateUniqueCharID[0]
		}
		for _, group := range append(append(append([]uint64(nil), main...), sub...), private...) {
			if group == 0 {
				rows.Close()
				return nil, fmt.Errorf("gamedata: equipment %d references option group zero", id)
			}
			referencedGroups[group] = true
		}
		d.Equipment[id] = item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = db.Query("SELECT id,ProtoBuf FROM EquipmentOptionRerollTable")
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
		protoID, idErr := packedInts(proto, 1)
		locks, lockErr := packedInts(proto, 2)
		counts, countErr := packedInts(proto, 3)
		ids, itemErr := packedInts(proto, 4)
		types, typeErr := packedInts(proto, 5)
		if idErr != nil || lockErr != nil || countErr != nil || itemErr != nil || typeErr != nil ||
			len(protoID) != 1 || protoID[0] != id || len(counts) == 0 || len(locks) != len(counts) || len(ids) != len(counts) || len(types) != len(counts) {
			rows.Close()
			return nil, fmt.Errorf("gamedata: option-reroll cost %d has mismatched resource arrays", id)
		}
		cost := EquipmentOptionRerollCost{Resources: make([]EquipmentOptionRerollResource, len(counts))}
		for i := range counts {
			if types[i] == 0 || counts[i] == 0 {
				rows.Close()
				return nil, fmt.Errorf("gamedata: option-reroll cost %d has invalid resource %d", id, i)
			}
			cost.Resources[i] = EquipmentOptionRerollResource{Type: types[i], ID: ids[i], BaseCount: counts[i], LockCount: locks[i]}
		}
		d.Costs[id] = cost
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = db.Query("SELECT id,ProtoBuf FROM EquipmentRerollDefaultTable")
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
		ratio, ratioErr := packedInts(proto, 1)
		sourceID, sourceIDErr := packedInts(proto, 3)
		sourceType, sourceTypeErr := packedInts(proto, 4)
		targetID, targetIDErr := packedInts(proto, 5)
		targetType, targetTypeErr := packedInts(proto, 6)
		if d.Conversion != nil || ratioErr != nil || sourceIDErr != nil || sourceTypeErr != nil || targetIDErr != nil || targetTypeErr != nil ||
			len(ratio) != 1 || ratio[0] == 0 || len(sourceID) != 1 || sourceID[0] == 0 || len(sourceType) != 1 || sourceType[0] == 0 ||
			len(targetID) != 1 || targetID[0] == 0 || len(targetType) != 1 || targetType[0] == 0 ||
			(sourceID[0] == targetID[0] && sourceType[0] == targetType[0]) {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment reroll default %d is invalid", id)
		}
		d.Conversion = &EquipmentOptionRerollConversion{
			Ratio: ratio[0], SourceType: sourceType[0], SourceID: sourceID[0], TargetType: targetType[0], TargetID: targetID[0],
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = db.Query("SELECT groupId,id,ProtoBuf FROM EquipmentOptionTable")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var groupID, id uint64
		var proto []byte
		if err := rows.Scan(&groupID, &id, &proto); err != nil {
			rows.Close()
			return nil, err
		}
		if !referencedGroups[groupID] {
			continue
		}
		protoGroup, groupErr := packedInts(proto, 3)
		protoID, idErr := packedInts(proto, 5)
		weights, weightErr := packedInts(proto, 2)
		value, present, valueErr := fixed64Double(proto, 1)
		if groupErr != nil || idErr != nil || weightErr != nil || valueErr != nil || !present ||
			len(protoGroup) != 1 || protoGroup[0] != groupID || len(protoID) != 1 || protoID[0] != id || len(weights) != 1 || weights[0] == 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			rows.Close()
			return nil, fmt.Errorf("gamedata: option group %d choice %d is invalid", groupID, id)
		}
		group := d.Groups[groupID]
		group.ID = groupID
		group.Choices = append(group.Choices, WeightedOption{ID: id, Weight: weights[0]})
		d.Groups[groupID] = group
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	for id, equipment := range d.Equipment {
		if len(d.Costs[equipment.OptionRerollID].Resources) == 0 {
			return nil, fmt.Errorf("gamedata: equipment %d references missing option-reroll cost %d", id, equipment.OptionRerollID)
		}
		for _, groupID := range append(append(append([]uint64(nil), equipment.MainGroups...), equipment.SubGroups...), equipment.PrivateGroups...) {
			if len(d.Groups[groupID].Choices) == 0 {
				return nil, fmt.Errorf("gamedata: equipment %d references missing option group %d", id, groupID)
			}
		}
	}
	return d, nil
}

func (d *EquipmentOptionRerollDesign) Lookup(equipmentID uint64) (EquipmentOptionRerollItem, bool) {
	if d == nil {
		return EquipmentOptionRerollItem{}, false
	}
	item, ok := d.Equipment[equipmentID]
	return item, ok
}

func (d *EquipmentOptionRerollDesign) Cost(equipmentID, lockedSlots uint64) ([]PromotionCost, error) {
	if d == nil {
		return nil, fmt.Errorf("gamedata: equipment option-reroll design unavailable")
	}
	equipment, ok := d.Equipment[equipmentID]
	if !ok {
		return nil, fmt.Errorf("gamedata: unknown equipment %d", equipmentID)
	}
	cost, ok := d.Costs[equipment.OptionRerollID]
	if !ok || len(cost.Resources) == 0 {
		return nil, fmt.Errorf("gamedata: equipment %d has no option-reroll cost", equipmentID)
	}
	result := make([]PromotionCost, len(cost.Resources))
	for i, resource := range cost.Resources {
		if resource.LockCount != 0 && lockedSlots > (math.MaxUint64-resource.BaseCount)/resource.LockCount {
			return nil, fmt.Errorf("gamedata: equipment %d option-reroll cost overflows", equipmentID)
		}
		result[i] = PromotionCost{Type: resource.Type, ID: resource.ID, Count: resource.BaseCount + resource.LockCount*lockedSlots}
	}
	return result, nil
}

// RollUnlocked independently rolls each unlocked slot from its own GameData
// option group. Locked slots are returned as zero choices for the caller to
// preserve; no result is excluded because it appeared in another slot or in
// the current equipment state.
func (d *EquipmentOptionRerollDesign) RollUnlocked(equipmentID uint64, locks EquipmentOptionRerollLocks) (EquipmentOptionRerollRoll, error) {
	if d == nil {
		return EquipmentOptionRerollRoll{}, fmt.Errorf("gamedata: equipment option-reroll design unavailable")
	}
	equipment, ok := d.Equipment[equipmentID]
	if !ok {
		return EquipmentOptionRerollRoll{}, fmt.Errorf("gamedata: unknown equipment %d", equipmentID)
	}
	if len(locks.Main) != len(equipment.MainGroups) || len(locks.Sub) != len(equipment.SubGroups) || len(locks.Private) != len(equipment.PrivateGroups) {
		return EquipmentOptionRerollRoll{}, fmt.Errorf("gamedata: equipment %d option lock arrays do not match slots", equipmentID)
	}
	result := EquipmentOptionRerollRoll{
		Main:    make([]EquipmentOptionChoice, len(equipment.MainGroups)),
		Sub:     make([]EquipmentOptionChoice, len(equipment.SubGroups)),
		Private: make([]EquipmentOptionChoice, len(equipment.PrivateGroups)),
	}
	for _, set := range []struct {
		groups []uint64
		locks  []bool
		out    []EquipmentOptionChoice
	}{{equipment.MainGroups, locks.Main, result.Main}, {equipment.SubGroups, locks.Sub, result.Sub}, {equipment.PrivateGroups, locks.Private, result.Private}} {
		for i, groupID := range set.groups {
			if set.locks[i] {
				continue
			}
			choice, err := d.rollGroup(groupID)
			if err != nil {
				return EquipmentOptionRerollRoll{}, fmt.Errorf("gamedata: roll equipment %d option group %d: %w", equipmentID, groupID, err)
			}
			set.out[i] = choice
		}
	}
	return result, nil
}

func (d *EquipmentOptionRerollDesign) rollGroup(groupID uint64) (EquipmentOptionChoice, error) {
	group, ok := d.Groups[groupID]
	if !ok || len(group.Choices) == 0 {
		return EquipmentOptionChoice{}, fmt.Errorf("missing option group %d", groupID)
	}
	pool := make([]WeightedEquipment, 0, len(group.Choices))
	for _, choice := range group.Choices {
		if choice.Weight == 0 {
			continue
		}
		pool = append(pool, WeightedEquipment{ID: choice.ID, Weight: choice.Weight})
	}
	draw := d.draw
	if draw == nil {
		draw = cryptoDraw
	}
	id, err := rollEquipmentChoiceWith(pool, draw)
	if err != nil {
		return EquipmentOptionChoice{}, err
	}
	return EquipmentOptionChoice{GroupID: groupID, ID: id}, nil
}
