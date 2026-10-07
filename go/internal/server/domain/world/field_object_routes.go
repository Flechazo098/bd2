package world

import (
	"bd2server/internal/server/domain/command"
	"fmt"
)

func (s *Service) fieldObjectCurrentPack(pack int) bool {

	current := s.activePack

	if current == 0 {
		current = s.state.ActivePackID()
	}
	return current == pack
}

func (s *Service) validateFieldObjectMap(ctx command.Context, pack, mapID int) error {
	_, saved := s.state.Position()
	_, event, err := s.resolveEventFieldPack(ctx, pack)
	if err != nil {
		return err
	}
	if !saved && !event {
		return nil
	}
	current, err := s.currentFieldMap(ctx, pack)
	if err != nil || current != mapID {
		return fmt.Errorf("%w: field object outside current map", ErrInvalidRequest)
	}
	return nil
}

func (s *Service) rewardMonsterAvailable(ctx command.Context, pack, monster int) (bool, error) {
	if s.fieldObjectLoader == nil && s.fieldObjects == nil {
		return true, nil
	}
	design, err := s.fieldObjectDesign(ctx, pack)
	if err != nil {
		return false, err
	}
	linked := false
	for _, obj := range design.Objects {
		if obj.MonsterID != monster || obj.BuffID != 0 || len(obj.Rewards) != 0 {
			continue
		}
		linked = true
		period, e := s.fieldObjectPeriodFor(pack, obj)
		if e != nil {
			continue
		}
		opened, e := s.state.FieldRewardOpened(ctx, pack, obj.ID, period)
		if e != nil {
			return false, e
		}
		if opened {
			return true, nil
		}
	}
	return !linked, nil
}
