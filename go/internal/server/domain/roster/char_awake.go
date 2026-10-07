package roster

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"errors"
	"fmt"
)

type CharAwakeService struct {
	design     *gamedata.CharAwakeDesign
	collection *CollectionStore
	characters *CharacterStore
	inventory  *assets.Inventory
	wallet     *assets.Wallet
}

func NewCharAwakeService(design *gamedata.CharAwakeDesign, collection *CollectionStore, characters *CharacterStore, inventory *assets.Inventory, wallet *assets.Wallet) (*CharAwakeService, error) {
	if design == nil || collection == nil || characters == nil || inventory == nil || wallet == nil {
		return nil, errors.New("player: incomplete character awakening service")
	}
	for uniqueID, progress := range collection.CharAwakeStates() {
		if err := design.ValidateProgress(uniqueID, progress.ImprintLevels, progress.IsAwake); err != nil {
			return nil, fmt.Errorf("player: invalid saved character awakening progress: %w", err)
		}
	}
	return &CharAwakeService{design: design, collection: collection, characters: characters, inventory: inventory, wallet: wallet}, nil
}

func (s *CharAwakeService) validateCosts(costs []gamedata.CharAwakeCost, materials []assets.Item) ([]assets.Item, uint64, error) {
	want := make(map[[2]uint64]uint64, len(costs))
	for _, cost := range costs {
		key := [2]uint64{cost.Type, cost.ID}
		if cost.Count > ^uint64(0)-want[key] {
			return nil, 0, errors.New("character awakening cost overflow")
		}
		want[key] += cost.Count
	}
	got := make(map[[2]uint64]uint64, len(materials))
	var items []assets.Item
	var gold uint64
	for _, material := range materials {
		key := [2]uint64{material.Type, material.ID}
		switch material.Type {
		case 4:
			if material.InvenIndex != 0 || material.ID != 0 || gold != 0 {
				return nil, 0, errors.New("invalid character awakening currency")
			}
			gold = material.Count
		case 8:
			items = append(items, material)
		default:
			return nil, 0, fmt.Errorf("unsupported character awakening material type %d", material.Type)
		}
		if material.Count > ^uint64(0)-got[key] {
			return nil, 0, errors.New("submitted character awakening material overflow")
		}
		got[key] += material.Count
	}
	if len(got) != len(want) {
		return nil, 0, fmt.Errorf("material kinds mismatch: request=%v GameData=%v", got, want)
	}
	for key, count := range want {
		if got[key] != count {
			return nil, 0, fmt.Errorf("material %d/%d=%d want %d", key[0], key[1], got[key], count)
		}
	}
	return items, gold, nil
}

// Contributions exposes server-side derived stats for CharInfo, revival and
// battle calculators. The client independently computes the same values from
// CharAwakeInfo, so neither side relies on a persisted derived number.
func (s *CharAwakeService) Contributions(ctx command.Context, character Character) ([]gamedata.StatContribution, error) {
	uniqueID, ok := s.design.CharacterUniqueID(character.ID)
	if !ok {
		return nil, nil
	}
	progress, _ := s.collection.CharAwakeState(uniqueID)
	return s.design.CharAwakeContributions(uniqueID, progress.ImprintLevels, progress.IsAwake)
}
