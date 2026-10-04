package gamedata

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// FieldObjectDesign retains the client table identities, including unsupported
// reward graphs, so callers can reject them without inventing a reward.
type FieldObjectDesign struct {
	Objects   map[int]FieldRewardObject
	Equipment *EquipmentGachaCatalog
}
type FieldRewardObject struct {
	ID, MapID, GroupID, Type, ResetType, QuestID, BuffID, MonsterID int
	Rewards                                                         []BattleReward
	Ratios                                                          []uint64
	DropCount, DropType                                             uint64
}

func LoadFieldObjects(root, version string, pack int) (FieldObjectDesign, error) {
	plain, err := ReadDatabase(root, version, fmt.Sprintf("pack%d", pack))
	if err != nil {
		return FieldObjectDesign{}, err
	}
	dir, err := os.MkdirTemp("", "bd2-field-objects-")
	if err != nil {
		return FieldObjectDesign{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "pack.db")
	if err = os.WriteFile(path, plain, 0600); err != nil {
		return FieldObjectDesign{}, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return FieldObjectDesign{}, err
	}
	defer db.Close()
	common, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return FieldObjectDesign{}, err
	}
	defer closeDB()
	return loadFieldObjects(db, common)
}
func loadFieldObjects(db, common *sql.DB) (FieldObjectDesign, error) {
	design := FieldObjectDesign{Objects: map[int]FieldRewardObject{}, Equipment: &EquipmentGachaCatalog{equipment: map[uint64]EquipmentDesign{}}}
	groups := map[int]FieldRewardObject{}
	rows, err := db.Query("SELECT id,ProtoBuf FROM FieldRewardObjectGroupTable")
	if err != nil {
		return design, err
	}
	for rows.Next() {
		var id int
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return design, err
		}
		obj := FieldRewardObject{GroupID: id}
		for f, target := range map[int]*int{1: &obj.BuffID, 7: &obj.MonsterID, 8: &obj.QuestID, 9: &obj.ResetType, 12: &obj.Type} {
			v, e := packedInts(raw, f)
			if e != nil || len(v) > 1 {
				rows.Close()
				return design, fmt.Errorf("gamedata: malformed field object group %d", id)
			}
			if len(v) > 0 {
				*target = int(v[0])
			}
		}
		group, e := packedInts(raw, 10)
		if e != nil || len(group) > 1 {
			rows.Close()
			return design, fmt.Errorf("gamedata: malformed field reward group %d", id)
		}
		if len(group) == 1 {
			var rewardRaw []byte
			if err = common.QueryRow("SELECT ProtoBuf FROM RewardGroupTable WHERE id=?", group[0]).Scan(&rewardRaw); err != nil {
				rows.Close()
				return design, err
			}
			ids, e1 := packedInts(rewardRaw, 5)
			types, e2 := packedInts(rewardRaw, 6)
			counts, e3 := packedInts(rewardRaw, 4)
			drop, e4 := packedInts(rewardRaw, 1)
			ratios, e5 := packedInts(rewardRaw, 8)
			dropType, e6 := packedInts(rewardRaw, 2)
			if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil {
				rows.Close()
				return design, fmt.Errorf("gamedata: malformed field loot %d", group[0])
			}
			if len(ids) > 0 && len(ids) == len(types) && len(ids) == len(counts) && len(ratios) == len(ids) && len(drop) <= 1 && len(dropType) <= 1 {
				if len(drop) == 1 {
					obj.DropCount = drop[0]
				}
				obj.Ratios = ratios
				if len(dropType) > 0 {
					obj.DropType = dropType[0]
				}
				for i := range ids {
					obj.Rewards = append(obj.Rewards, BattleReward{ID: ids[i], Type: types[i], Count: counts[i]})
					if types[i] == 10 {
						if e := design.Equipment.loadEquipmentTree(common, WeightedEquipment{ID: ids[i]}); e != nil {
							rows.Close()
							return design, e
						}
					}
				}
			} else {
				rows.Close()
				return design, fmt.Errorf("gamedata: malformed field loot entries %d", group[0])
			}
		}
		groups[id] = obj
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return design, err
	}
	rows.Close()
	rows, err = db.Query("SELECT id,ProtoBuf FROM FieldRewardObjectTable")
	if err != nil {
		return design, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return design, err
		}
		group, e1 := packedInts(raw, 3)
		maps, e2 := packedInts(raw, 6)
		if e1 != nil || e2 != nil || len(group) != 1 || len(maps) > 1 || id <= 0 {
			return design, fmt.Errorf("gamedata: malformed field object %d", id)
		}
		obj, ok := groups[int(group[0])]
		if !ok {
			return design, fmt.Errorf("gamedata: missing object group %d", group[0])
		}
		obj.ID = id
		if len(maps) > 0 {
			obj.MapID = int(maps[0])
		}
		design.Objects[id] = obj
	}
	return design, rows.Err()
}

// DropType 0 makes DropCount weighted selections. DropType 1 checks each
// component's percentage independently; its DropCount may be proto default zero.
func (o FieldRewardObject) Draw() ([]BattleReward, error) {
	return o.draw(cryptoDraw)
}

func (o FieldRewardObject) draw(draw func(uint64) (uint64, error)) ([]BattleReward, error) {
	if o.DropType > 1 || (o.DropType == 0 && o.DropCount == 0) || o.DropCount > 100 || len(o.Rewards) == 0 || len(o.Rewards) != len(o.Ratios) {
		return nil, fmt.Errorf("gamedata: unsupported field reward draw")
	}
	var total uint64
	for i, r := range o.Ratios {
		if o.Rewards[i].Count == 0 || o.Rewards[i].Count > uint64(^uint32(0)>>1) || (o.DropType == 1 && r > 100) {
			return nil, fmt.Errorf("gamedata: malformed field reward component")
		}
		if ^uint64(0)-total < r {
			return nil, fmt.Errorf("gamedata: reward ratio overflow")
		}
		total += r
	}
	if total == 0 && o.DropType == 0 {
		return nil, fmt.Errorf("gamedata: empty field reward distribution")
	}
	var result []BattleReward
	if o.DropType == 1 {
		for i, ratio := range o.Ratios {
			if ratio == 0 {
				continue
			}
			if ratio < 100 {
				value, err := draw(100)
				if err != nil {
					return nil, err
				}
				if value >= 100 {
					return nil, fmt.Errorf("gamedata: field reward random out of range")
				}
				if value >= ratio {
					continue
				}
			}
			result = append(result, o.Rewards[i])
		}
		return result, nil
	}
	for n := uint64(0); n < o.DropCount; n++ {
		value, err := draw(total)
		if err != nil {
			return nil, err
		}
		if value >= total {
			return nil, fmt.Errorf("gamedata: field reward random out of range")
		}
		pick := value
		for i, ratio := range o.Ratios {
			if pick < ratio {
				result = append(result, o.Rewards[i])
				break
			}
			pick -= ratio
		}
	}
	return result, nil
}
