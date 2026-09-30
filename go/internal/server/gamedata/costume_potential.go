package gamedata

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

type CostumePotentialCost struct{ Type, ID, Count uint64 }

type CostumePotentialNode struct {
	ID             uint64
	ConditionGrade uint64
	Prerequisites  []uint64
	Costs          []CostumePotentialCost
}

type CostumePotentialDesign struct {
	Nodes           map[uint64]map[uint64]CostumePotentialNode
	CostumeUnique   map[uint64]uint64
	CharacterGrade  map[uint64]uint64
	CharacterUnique map[uint64]uint64
}

func LoadCostumePotentialDesign(root, version string) (*CostumePotentialDesign, error) {
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-costume-potential-")
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
	return loadCostumePotentialDesign(db)
}

func loadCostumePotentialDesign(db *sql.DB) (*CostumePotentialDesign, error) {
	d := &CostumePotentialDesign{Nodes: map[uint64]map[uint64]CostumePotentialNode{}, CostumeUnique: map[uint64]uint64{}, CharacterGrade: map[uint64]uint64{}, CharacterUnique: map[uint64]uint64{}}
	rows, err := db.Query("SELECT groupId,id,ProtoBuf FROM CostumeNodeTable ORDER BY groupId,id")
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
		counts, _ := packedInts(proto, 1)
		ids, _ := packedInts(proto, 2)
		types, _ := packedInts(proto, 3)
		if len(counts) == 0 || len(counts) != len(ids) || len(counts) != len(types) {
			rows.Close()
			return nil, fmt.Errorf("gamedata: invalid costume potential costs %d/%d", groupID, id)
		}
		grade, _ := packedInts(proto, 17)
		conditions, _ := packedInts(proto, 18)
		node := CostumePotentialNode{ID: id, Prerequisites: append([]uint64(nil), conditions...)}
		if len(grade) == 1 {
			node.ConditionGrade = grade[0]
		} else if len(grade) > 1 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: invalid costume potential grade %d/%d", groupID, id)
		}
		for i := range counts {
			if counts[i] == 0 || types[i] == 0 || (types[i] != 4 && ids[i] == 0) {
				rows.Close()
				return nil, fmt.Errorf("gamedata: invalid costume potential cost %d/%d", groupID, id)
			}
			node.Costs = append(node.Costs, CostumePotentialCost{Type: types[i], ID: ids[i], Count: counts[i]})
		}
		if d.Nodes[groupID] == nil {
			d.Nodes[groupID] = map[uint64]CostumePotentialNode{}
		}
		d.Nodes[groupID][id] = node
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT id,ProtoBuf FROM CostumeTable")
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
		unique, _ := packedInts(proto, 27)
		if len(unique) == 1 {
			d.CostumeUnique[id] = unique[0]
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT id,ProtoBuf FROM CharTable")
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
		grade, _ := packedInts(proto, 10)
		unique, _ := packedInts(proto, 20)
		if len(grade) == 1 && len(unique) == 1 {
			d.CharacterGrade[id] = grade[0]
			d.CharacterUnique[id] = unique[0]
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(d.Nodes) == 0 {
		return nil, fmt.Errorf("gamedata: CostumeNodeTable is empty")
	}
	return d, nil
}

func (d *CostumePotentialDesign) Validate(costumeID, characterID, grade uint64, active, requested []uint64) ([]CostumePotentialCost, error) {
	if d == nil || costumeID == 0 || characterID == 0 || len(requested) == 0 {
		return nil, fmt.Errorf("gamedata: invalid costume potential request")
	}
	if d.CostumeUnique[costumeID] == 0 || d.CostumeUnique[costumeID] != d.CharacterUnique[characterID] {
		return nil, fmt.Errorf("gamedata: costume %d does not belong to character %d", costumeID, characterID)
	}
	if grade == 0 {
		grade = d.CharacterGrade[characterID]
	}
	known := make(map[uint64]bool, len(active)+len(requested))
	for _, id := range active {
		known[id] = true
	}
	for _, id := range requested {
		if id == 0 || known[id] {
			return nil, fmt.Errorf("gamedata: duplicate costume potential node %d", id)
		}
		known[id] = true
	}
	var costs []CostumePotentialCost
	for _, id := range requested {
		node, exists := d.Nodes[costumeID][id]
		if !exists {
			return nil, fmt.Errorf("gamedata: costume %d has no potential node %d", costumeID, id)
		}
		if node.ConditionGrade > grade {
			return nil, fmt.Errorf("gamedata: potential node %d requires growth grade %d", id, node.ConditionGrade)
		}
		for _, prerequisite := range node.Prerequisites {
			if !known[prerequisite] {
				return nil, fmt.Errorf("gamedata: potential node %d requires node %d", id, prerequisite)
			}
		}
		costs = append(costs, node.Costs...)
	}
	return costs, nil
}
