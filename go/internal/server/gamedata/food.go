package gamedata

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
)

// Food is decoded from FoodTable, including materials so that the server can
// reject them explicitly. FavoritePoint replaces Point for matching characters.
type Food struct {
	ID, Type, Point, RecoveryType, FavoritePoint, FoodBuffID uint64
	FavoriteUniqueCharIDs                                    []uint64
}

type FoodDesign struct{ Foods map[uint64]Food }

func LoadFoodDesign(root, version string) (*FoodDesign, error) {
	db, release, err := OpenDatabase(root, version, "common")
	if err != nil {
		return nil, err
	}
	defer release()
	return loadFoodDesign(db)
}

func loadFoodDesign(db *sql.DB) (*FoodDesign, error) {
	rows, err := db.Query("SELECT id,ProtoBuf FROM FoodTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	design := &FoodDesign{Foods: map[uint64]Food{}}
	for rows.Next() {
		var id uint64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		read := func(field int) (uint64, error) {
			values, err := packedInts(raw, field)
			if err != nil || len(values) > 1 {
				return 0, fmt.Errorf("gamedata: invalid food %d field %d", id, field)
			}
			if len(values) == 0 {
				return 0, nil
			}
			if values[0] > math.MaxInt32 {
				return 0, fmt.Errorf("gamedata: invalid food %d integer", id)
			}
			return values[0], nil
		}
		food := Food{ID: id}
		for field, target := range map[int]*uint64{1: &food.FavoritePoint, 3: &food.FoodBuffID, 4: &food.Type, 13: &food.Point, 14: &food.RecoveryType} {
			if *target, err = read(field); err != nil {
				return nil, err
			}
		}
		protoID, err := read(7)
		if err != nil || id == 0 || protoID != id || food.Type > 2 || food.RecoveryType > 1 {
			return nil, fmt.Errorf("gamedata: invalid food row %d", id)
		}
		food.FavoriteUniqueCharIDs, err = packedInts(raw, 2)
		if err != nil {
			return nil, err
		}
		for _, charID := range food.FavoriteUniqueCharIDs {
			if charID == 0 || charID > math.MaxInt32 {
				return nil, fmt.Errorf("gamedata: invalid food %d favorite", id)
			}
		}
		if food.Type != 2 && food.Point == 0 && food.FoodBuffID == 0 {
			return nil, fmt.Errorf("gamedata: edible food %d has no effect", id)
		}
		design.Foods[id] = food
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(design.Foods) == 0 {
		return nil, errors.New("gamedata: empty FoodTable")
	}
	return design, nil
}

// Recovery uses the client's float multiplication and Math.Round midpoint to
// even semantics once per item, then multiplies by the requested stack count.
func (f Food) Recovery(characterID, maximum, count uint64) (uint64, error) {
	if f.Type > 1 || f.RecoveryType > 1 || f.FoodBuffID != 0 || count == 0 || count > math.MaxInt32 || maximum == 0 {
		return 0, errors.New("gamedata: food is not a supported recovery dish")
	}
	point := f.Point
	if f.Type == 1 {
		for _, favorite := range f.FavoriteUniqueCharIDs {
			if favorite == characterID/10 {
				point = f.FavoritePoint
				break
			}
		}
	}
	if point == 0 {
		return 0, errors.New("gamedata: food has no recovery effect")
	}
	if f.RecoveryType == 1 {
		value := math.RoundToEven(float64(float32(maximum) * (float32(point) / float32(100))))
		if value < 0 || value >= float64(math.MaxUint64) {
			return 0, errors.New("gamedata: food recovery overflow")
		}
		point = uint64(value)
	}
	if point != 0 && count > math.MaxUint64/point {
		return 0, errors.New("gamedata: food recovery overflow")
	}
	return point * count, nil
}
