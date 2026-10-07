package gamedata

import (
	"errors"
	"fmt"

	_ "modernc.org/sqlite"
)

// LimitedCostumeCatalog is the authoritative LimitedCostumeTable membership
// plus the character/growth design required to grant those costumes. It is a
// reward lookup only and never changes any gacha pool.
type LimitedCostumeCatalog struct {
	ids        []uint64
	characters map[uint64]CharacterDesign
}

func LoadLimitedCostumes(root, version string) (*LimitedCostumeCatalog, error) {
	db, release, err := OpenDatabase(root, version, "common")
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := db.Query("SELECT id FROM LimitedCostumeTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	catalog := &LimitedCostumeCatalog{characters: map[uint64]CharacterDesign{}}
	for rows.Next() {
		var costumeID uint64
		if err := rows.Scan(&costumeID); err != nil {
			return nil, err
		}
		if costumeID == 0 {
			return nil, errors.New("gamedata: limited costume has zero id")
		}
		catalog.ids = append(catalog.ids, costumeID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Release the single connection before character loading performs queries.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(catalog.ids) == 0 {
		return nil, errors.New("gamedata: limited costume catalog is empty")
	}
	for _, costumeID := range catalog.ids {
		design, err := loadGachaCharacterDesign(db, costumeID)
		if err != nil {
			return nil, fmt.Errorf("gamedata: limited costume %d: %w", costumeID, err)
		}
		catalog.characters[costumeID] = design
	}
	return catalog, nil
}

func (c *LimitedCostumeCatalog) IDs() []uint64 {
	if c == nil {
		return nil
	}
	return append([]uint64(nil), c.ids...)
}

func (c *LimitedCostumeCatalog) Character(costumeID uint64) (CharacterDesign, bool) {
	if c == nil {
		return CharacterDesign{}, false
	}
	design, ok := c.characters[costumeID]
	return design, ok
}

// Excluding returns stable LimitedCostumeTable order without the supplied
// currently featured pickup costume IDs.
func (c *LimitedCostumeCatalog) Excluding(excluded map[uint64]bool) []uint64 {
	if c == nil {
		return nil
	}
	ids := make([]uint64, 0, len(c.ids))
	for _, id := range c.ids {
		if !excluded[id] {
			ids = append(ids, id)
		}
	}
	return ids
}
