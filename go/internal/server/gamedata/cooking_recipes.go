package gamedata

import (
	"database/sql"
	"fmt"
	"math"
)

// CookingRecipeDesign describes valid recipe identities. Learning a recipe is
// player state, so listing this table never grants all of its recipes.
type CookingRecipeDesign struct{ IDs map[uint64]bool }

func LoadCookingRecipeDesign(root, version string) (*CookingRecipeDesign, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	return loadCookingRecipeDesign(db)
}

func loadCookingRecipeDesign(db *sql.DB) (*CookingRecipeDesign, error) {
	rows, err := db.Query("SELECT id FROM CookingTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	design := &CookingRecipeDesign{IDs: map[uint64]bool{}}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id <= 0 || id > math.MaxInt32 || design.IDs[uint64(id)] {
			return nil, fmt.Errorf("gamedata: invalid cooking recipe identity")
		}
		design.IDs[uint64(id)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(design.IDs) == 0 {
		return nil, fmt.Errorf("gamedata: empty cooking recipe catalog")
	}
	return design, nil
}
