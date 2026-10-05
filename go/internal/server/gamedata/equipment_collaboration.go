package gamedata

import (
	"database/sql"
	"fmt"
	"sort"
)

// IncludeCollaborationURWeapons extends only the UR equipment ticket pool.
// Limited costumes identify eligible owners; equipment IDs remain version data.
func (c *EquipmentGachaCatalog) IncludeCollaborationURWeapons(root, version string) error {
	db, release, err := OpenDatabase(root, version, "common")
	if err != nil {
		return err
	}
	defer release()
	return c.includeCollaborationURWeapons(db)
}

func (c *EquipmentGachaCatalog) includeCollaborationURWeapons(db *sql.DB) error {
	if c == nil || c.equipment == nil {
		return fmt.Errorf("gamedata: missing equipment catalog")
	}
	// Select guaranteed weapon products from their existing equipment definitions,
	// rather than tying eligibility to a product or resource-ticket number.
	var targets []uint64
	for id, g := range c.Gachas {
		if !g.TicketOnly || len(g.TicketIDs) == 0 || len(g.Pool) == 0 {
			continue
		}
		eligible := true
		for _, branch := range g.Pool {
			if branch.ID != 0 || branch.Weight == 0 || len(branch.Children) == 0 {
				eligible = false
				break
			}
			for _, item := range branch.Children {
				if item.ID == 0 || len(item.Children) != 0 {
					eligible = false
					break
				}
			}
		}
		if !eligible {
			continue
		}
		var visit func([]WeightedEquipment) error
		visit = func(pool []WeightedEquipment) error {
			for _, entry := range pool {
				if entry.ID == 0 {
					if len(entry.Children) == 0 {
						eligible = false
					}
					if err := visit(entry.Children); err != nil {
						return err
					}
					continue
				}
				var raw []byte
				if err := db.QueryRow("SELECT ProtoBuf FROM EquipmentTable WHERE id=?", entry.ID).Scan(&raw); err != nil {
					return err
				}
				grade, e1 := packedInts(raw, 3)
				quality, e2 := packedInts(raw, 18)
				owner, e3 := packedInts(raw, 16)
				if e1 != nil || e2 != nil || e3 != nil {
					return fmt.Errorf("gamedata: malformed ticket equipment %d", entry.ID)
				}
				if len(grade) != 1 || grade[0] != 4 || len(quality) != 1 || quality[0] != 3 || len(owner) != 1 || owner[0] == 0 {
					eligible = false
				}
			}
			return nil
		}
		if err := visit(g.Pool); err != nil {
			return err
		}
		if eligible {
			targets = append(targets, id)
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i] < targets[j] })
	// Publish only after all selected products validate successfully.
	clone := *c
	clone.Gachas = make(map[uint64]EquipmentGacha, len(c.Gachas))
	for id, g := range c.Gachas {
		clone.Gachas[id] = g
	}
	clone.equipment = make(map[uint64]EquipmentDesign, len(c.equipment))
	for id, d := range c.equipment {
		clone.equipment[id] = d
	}
	for _, id := range targets {
		if err := clone.includeCollaborationWeaponPool(db, id); err != nil {
			return err
		}
	}
	c.Gachas, c.equipment = clone.Gachas, clone.equipment
	return nil
}

func (c *EquipmentGachaCatalog) includeCollaborationWeaponPool(db *sql.DB, id uint64) error {
	g := c.Gachas[id]
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
			if grade == 0 || (branchGrade != 0 && branchGrade != grade) {
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
	g.Grades = make(map[uint64]uint64, len(seen))
	for id := range seen {
		g.Grades[id] = c.equipment[id].Grade
	}
	c.Gachas[g.ID] = g
	return nil
}
