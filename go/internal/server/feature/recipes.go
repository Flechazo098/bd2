package feature

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
)

type RecipeItems interface{ All() []player.Item }

type RecipeService struct {
	design  *gamedata.CookingRecipeDesign
	initial []uint64
	items   RecipeItems
}

func (s *RecipeService) Knows(id uint64) bool {
	if !s.design.IDs[id] {
		return false
	}
	for _, known := range s.initial {
		if known == id {
			return true
		}
	}
	for _, item := range s.items.All() {
		if item.Type == 7 && item.ID == id && item.Count > 0 {
			return true
		}
	}
	return false
}

func NewRecipeService(design *gamedata.CookingRecipeDesign, initial []uint64, items RecipeItems) (*RecipeService, error) {
	if design == nil || len(design.IDs) == 0 || items == nil {
		return nil, fmt.Errorf("recipes: missing design or inventory")
	}
	for _, id := range initial {
		if !design.IDs[id] {
			return nil, fmt.Errorf("recipes: initial recipe %d is absent from GameData", id)
		}
	}
	return &RecipeService{design: design, initial: append([]uint64(nil), initial...), items: items}, nil
}

func (s *RecipeService) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/RecipeInfo" {
		return 0, nil, false, nil
	}
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return 0, nil, true, ErrInvalidRequest
	}
	known := map[uint64]bool{}
	for _, id := range s.initial {
		known[id] = true
	}
	for _, item := range s.items.All() {
		// EElementType.CookingRecipe is protocol value 7. Ownership comes from
		// seed state or real reward grants, never from every row in CookingTable.
		if item.Type != 7 || item.Count == 0 {
			continue
		}
		if !s.design.IDs[item.ID] {
			return 0, nil, true, fmt.Errorf("recipes: owned recipe %d is absent from GameData", item.ID)
		}
		known[item.ID] = true
	}
	ids := make([]uint64, 0, len(known))
	for id := range known {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var packed []byte
	for _, id := range ids {
		packed = binary.AppendUvarint(packed, id)
	}
	if len(packed) == 0 {
		return 46, nil, true, nil
	}
	return 46, wire.AppendBytes(nil, 2, packed), true, nil
}
