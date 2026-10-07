package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"fmt"
)

func (s *Service) AttachResearchRuntime(source *gamedata.Source, chars map[uint64]bool, economy interface {
	Apply(ctx command.Context, _ string, _ []gamedata.Reward, _ []gamedata.Reward) ([]byte, error)
}) error {
	if economy == nil {
		return fmt.Errorf("world: nil research economy")
	}
	s.researchCharacters = chars
	s.researchEconomy = economy
	s.researchDesigns = map[int]gamedata.FieldResearchDesign{}
	s.researchLoader = func(pack int) (gamedata.FieldResearchDesign, error) {
		return source.FieldResearch(pack)
	}
	return nil
}
func (s *Service) researchDesign(pack int) (gamedata.FieldResearchDesign, error) {
	if d, ok := s.researchDesigns[pack]; ok {
		return d, nil
	}
	if s.researchLoader == nil {
		return gamedata.FieldResearchDesign{}, fmt.Errorf("world: research design unavailable")
	}
	d, err := s.researchLoader(pack)
	if err != nil {
		return d, err
	}
	s.researchDesigns[pack] = d
	return d, nil
}

func matchesFieldCount(obj gamedata.FieldRewardObject, category uint64) bool {
	// PackMapRewardInfo uses independent predicates; a normal or hidden box
	// with a one-time reset belongs in both once and acquisition totals.
	switch category {
	case 2:
		return obj.ResetType == 1 && obj.Type != 5
	case 3:
		return obj.Type == 1 || obj.Type == 3
	case 4:
		return obj.Type == 6 && obj.ResetType == 2
	}
	return false
}
