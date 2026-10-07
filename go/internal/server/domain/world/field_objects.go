package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"fmt"
)

func (s *Service) WithFieldObjects(designs map[int]gamedata.FieldObjectDesign) *Service {
	s.fieldObjects = designs
	return s
}
func (s *Service) AttachFieldObjectRuntime(source *gamedata.Source, schedule gamedata.FieldResetSchedule) error {
	s.fieldReset = schedule
	s.fieldObjectLoader = func(pack int) (gamedata.FieldObjectDesign, error) {
		return source.FieldObjects(pack)
	}
	s.fieldObjects = map[int]gamedata.FieldObjectDesign{}
	return nil
}
func (s *Service) fieldObjectDesign(ctx command.Context, pack int) (gamedata.FieldObjectDesign, error) {
	if design, ok := s.fieldObjects[pack]; ok {
		return design, nil
	}
	if s.fieldObjectLoader == nil {
		return gamedata.FieldObjectDesign{}, fmt.Errorf("world: field object design unavailable")
	}
	if _, story := s.packs[pack]; !story {
		if _, field := s.fieldPacks[pack]; !field {
			if _, event, err := s.resolveEventFieldPack(ctx, pack); err != nil || !event {
				return gamedata.FieldObjectDesign{}, fmt.Errorf("%w: unknown field pack", ErrInvalidRequest)
			}
		}
	}
	design, err := s.fieldObjectLoader(pack)
	if err != nil {
		return design, err
	}
	s.fieldObjects[pack] = design
	return design, nil
}
func (s *Service) openedFieldObjects(ctx command.Context, pack int) ([]int, error) {
	ids, err := s.state.OpenedFieldRewards(ctx, pack)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []int{}, nil
	}
	design, err := s.fieldObjectDesign(ctx, pack)
	if err != nil {
		return nil, err
	}
	var active []int
	for _, id := range ids {
		obj, ok := design.Objects[id]
		if !ok {
			return nil, fmt.Errorf("world: saved field object absent from design")
		}
		period, e := s.fieldObjectPeriodFor(pack, obj)
		if e != nil {
			continue
		}
		opened, e := s.state.FieldRewardOpened(ctx, pack, id, period)
		if e != nil {
			return nil, e
		}
		if opened {
			active = append(active, id)
		}
	}
	return active, nil
}
func (s *Service) WithFieldResetSchedule(schedule gamedata.FieldResetSchedule) *Service {
	s.fieldReset = schedule
	return s
}
func (s *Service) fieldObjectPeriod(obj gamedata.FieldRewardObject) (string, error) {
	return s.fieldReset.Period(obj.ResetType, s.monsterTime())
}
func (s *Service) fieldObjectPeriodFor(pack int, obj gamedata.FieldRewardObject) (string, error) {
	if obj.ResetType == 2 {
		resolver, ok := s.eventFieldPacks.(interface {
			FieldObjectEventPeriod(int) (string, int64, error)
		})
		if !ok {
			return "", fmt.Errorf("%w: field event calendar unavailable", ErrInvalidRequest)
		}
		period, _, err := resolver.FieldObjectEventPeriod(pack)
		if err != nil {
			return "", err
		}
		if period == "" {
			return "", fmt.Errorf("%w: field event inactive", ErrInvalidRequest)
		}
		return "event:" + period, nil
	}
	return s.fieldObjectPeriod(obj)
}
