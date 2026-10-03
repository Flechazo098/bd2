package player

import (
	"bd2server/internal/server/gamedata"
	"errors"
	"fmt"
	"math"
)

func (s *EquipmentInventory) AttachStatDesign(design *gamedata.EquipmentStatDesign) error {
	if design == nil {
		return errors.New("player: nil equipment stat design")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statDesign = design
	return nil
}

// StatContributions resolves only maximum-health equipment effects. Snapshot
// options under the equipment lock and release it before calculating; callers
// may themselves be the CharacterStore maximum-health callback.
func (s *EquipmentInventory) StatContributions(character Character) ([]gamedata.StatContribution, error) {
	if character.InvenIndex == 0 {
		return nil, errors.New("player: invalid equipment stat character")
	}
	type query struct {
		option gamedata.EquipmentOption
		sub    bool
	}
	var queries []query
	s.mu.Lock()
	design := s.statDesign
	for _, equipment := range s.owned.Equipment {
		if equipment.UseChar != character.InvenIndex {
			continue
		}
		if equipment.Level > math.MaxInt32 {
			s.mu.Unlock()
			return nil, fmt.Errorf("player: invalid equipment %d level", equipment.InvenIndex)
		}
		var rank [3]int
		for i, value := range equipment.Rank {
			if i >= 3 || value > 4 {
				s.mu.Unlock()
				return nil, fmt.Errorf("player: invalid equipment %d ranks", equipment.InvenIndex)
			}
			rank[i] = int(value)
		}
		add := func(option EquipmentOption, sub bool) {
			if option.ID == 1 || option.ID == 2 {
				queries = append(queries, query{gamedata.EquipmentOption{GroupID: option.GroupID, ID: option.ID, Level: int(equipment.Level), Rank: rank}, sub})
			}
		}
		for _, option := range equipment.MainOption {
			add(option, false)
		}
		for _, option := range equipment.SubOption {
			add(option, true)
		}
		if equipment.PrivateOption != nil {
			add(*equipment.PrivateOption, false)
		}
	}
	s.mu.Unlock()
	if len(queries) == 0 {
		return nil, nil
	}
	if design == nil {
		return nil, errors.New("player: equipment stat design unavailable")
	}
	result := make([]gamedata.StatContribution, 0, len(queries))
	for _, query := range queries {
		contribution, err := design.HealthContribution(query.option, query.sub)
		if err != nil {
			return nil, err
		}
		result = append(result, contribution)
	}
	return result, nil
}
