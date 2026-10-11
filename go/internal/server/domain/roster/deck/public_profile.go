package deck

import (
	"bd2server/internal/server/domain/command"
	"fmt"
)

func (s *Store) HighestTotalBattlePower() uint64 {
	return s.state.HighestTotalBattlePower
}

func (s *Store) SelectPortrait(ctx command.Context, id, defaultCostumeID uint64) error {
	if id != 0 && id != defaultCostumeID {
		if s.collection == nil {
			return fmt.Errorf("deck: missing portrait ownership")
		}
		if _, ok := s.collection.CostumeByID(id); !ok {
			return fmt.Errorf("deck: portrait costume is not owned")
		}
	}
	next := clone(s.state)
	next.PortraitCostumeID = id
	return s.commit(ctx, next)
}
