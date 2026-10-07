package inventory

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"errors"
	"fmt"
	"slices"
)

type RecipeItems interface {
	All(ctx command.Context) []Item
}

type RecipeService struct {
	design  *gamedata.CookingRecipeDesign
	initial []uint64
	items   RecipeItems
}

func (s *RecipeService) Knows(ctx command.Context, id uint64) bool {
	if !s.design.IDs[id] {
		return false
	}
	if slices.Contains(s.initial, id) {
		return true
	}
	for _, item := range s.items.All(ctx) {
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

var errInvalidRecipeRequest = errors.New("feature: invalid protobuf request")
