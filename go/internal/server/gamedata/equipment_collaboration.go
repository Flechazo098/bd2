package gamedata

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// IncludeCollaborationURWeapons extends only the UR equipment ticket pool.
// Limited costumes identify eligible owners; equipment IDs remain version data.
func (c *EquipmentGachaCatalog) IncludeCollaborationURWeapons(root, version string) error {
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "bd2-collaboration-equipment-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "common.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	return c.includeCollaborationURWeapons(db)
}

func (c *EquipmentGachaCatalog) includeCollaborationURWeapons(db *sql.DB) error {
	if c == nil || c.equipment == nil {
		return fmt.Errorf("gamedata: missing equipment catalog")
	}
	g, ok := c.Gachas[71200001]
	if !ok || !g.TicketOnly || len(g.TicketIDs) != 1 || g.TicketIDs[0] != 1104 || len(g.Pool) != 3 {
		return fmt.Errorf("gamedata: missing or malformed UR equipment ticket pool")
	}
	readRows := func(query string) (map[uint64][]byte, error) {
		rows, err := db.Query(query)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[uint64][]byte{}
		for rows.Next() {
			var id uint64
			var raw []byte
			if err := rows.Scan(&id, &raw); err != nil {
				return nil, err
			}
			out[id] = raw
		}
		return out, rows.Err()
	}
	scalar := func(raw []byte, field int) (uint64, error) {
		v, err := packedInts(raw, field)
		if err != nil || len(v) != 1 || v[0] == 0 {
			return 0, fmt.Errorf("gamedata: invalid collaboration equipment field %d", field)
		}
		return v[0], nil
	}
	owners, err := readRows("SELECT DISTINCT c.useUniqueCharId,c.ProtoBuf FROM LimitedCostumeTable l JOIN CostumeTable c ON c.id=l.id ORDER BY c.useUniqueCharId")
	if err != nil {
		return err
	}
	chars, err := readRows("SELECT id,ProtoBuf FROM CharTable WHERE uniqueCharId IN (SELECT privateUniqueCharId FROM EquipmentTable WHERE privateUniqueCharId>0) ORDER BY id")
	if err != nil {
		return err
	}
	grades := map[uint64]uint64{}
	for _, raw := range chars {
		owner, err := scalar(raw, 20)
		if err != nil {
			return err
		}
		grade, err := scalar(raw, 9)
		if err != nil {
			return err
		}
		if previous, exists := grades[owner]; exists && previous != grade {
			return fmt.Errorf("gamedata: conflicting character grade for %d", owner)
		}
		grades[owner] = grade
	}
	equipment, err := readRows("SELECT id,ProtoBuf FROM EquipmentTable ORDER BY id")
	if err != nil {
		return err
	}
	pool := make([]WeightedEquipment, len(g.Pool))
	branchForGrade := map[uint64]int{}
	seen := map[uint64]bool{}
	for index, branch := range g.Pool {
		if branch.ID != 0 || branch.Weight == 0 || len(branch.Children) == 0 {
			return fmt.Errorf("gamedata: malformed UR ticket branch")
		}
		pool[index] = WeightedEquipment{Weight: branch.Weight, Children: append([]WeightedEquipment(nil), branch.Children...)}
		var branchGrade uint64
		for _, item := range branch.Children {
			if item.ID == 0 || item.Weight != 1 || len(item.Children) != 0 || seen[item.ID] {
				return fmt.Errorf("gamedata: malformed or duplicate UR ticket candidate %d", item.ID)
			}
			seen[item.ID] = true
			owner, err := scalar(equipment[item.ID], 16)
			if err != nil {
				return err
			}
			grade := grades[owner]
			if grade < 3 || grade > 5 || (branchGrade != 0 && branchGrade != grade) {
				return fmt.Errorf("gamedata: unknown or mixed UR ticket character tier")
			}
			branchGrade = grade
		}
		if _, exists := branchForGrade[branchGrade]; exists {
			return fmt.Errorf("gamedata: duplicate UR ticket tier")
		}
		branchForGrade[branchGrade] = index
	}
	// Collect designs before publishing the cloned pool, so a failed load leaves
	// the catalog unchanged. Order by ID to keep repeated loads deterministic.
	designs := map[uint64]EquipmentDesign{}
	ids := make([]uint64, 0, len(equipment))
	for id := range equipment {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		raw := equipment[id]
		grade, _ := packedInts(raw, 3)
		quality, _ := packedInts(raw, 18)
		owner, _ := packedInts(raw, 16)
		if len(grade) != 1 || grade[0] != 4 || len(quality) != 1 || quality[0] != 3 || len(owner) != 1 {
			continue
		}
		if _, eligible := owners[owner[0]]; !eligible || seen[id] {
			continue
		}
		index, exists := branchForGrade[grades[owner[0]]]
		if !exists {
			return fmt.Errorf("gamedata: unknown collaboration equipment tier for %d", id)
		}
		design, err := loadEquipmentDesign(db, id)
		if err != nil {
			return err
		}
		designs[id] = design
		pool[index].Children = append(pool[index].Children, WeightedEquipment{ID: id, Weight: 1})
		seen[id] = true
	}
	for id, design := range designs {
		c.equipment[id] = design
	}
	g.Pool = pool
	c.Gachas[g.ID] = g
	return nil
}
