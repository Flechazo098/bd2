package eventactions

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"errors"
	"fmt"
)

type MiniContentResolver interface {
	ResolveMiniContentUID(ctx command.Context, _ uint64) (uint64, uint64, int64, int64, error)
	ListMiniContentRoutes(ctx command.Context) ([]gamedata.MiniContentRoute, error)
}

func (s *Service) AttachMiniContent(ctx command.Context, resolver MiniContentResolver, design *gamedata.MiniContentDesign) error {
	if resolver == nil || design == nil {
		return errors.New("eventactions: mini content dependencies missing")
	}
	routes, err := resolver.ListMiniContentRoutes(ctx)
	if err != nil {
		return err
	}
	for _, route := range routes {
		if route.ContentType == 14 {
			ids := design.Groups[route.ContentID]
			if len(ids) == 0 {
				return fmt.Errorf("eventactions: published mini story group %d missing", route.ContentID)
			}
			for _, id := range ids {
				if _, exists := design.Stories[id]; !exists {
					return fmt.Errorf("eventactions: published daily story %d missing", id)
				}
			}
		}
		if route.ContentType == 13 {
			found := false
			for _, row := range s.design.Tables["NpcQuizTable"] {
				if row.V(1) == route.ContentID {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("eventactions: published NPC quiz group %d missing", route.ContentID)
			}
		}
	}
	s.miniContent, s.miniDesign = resolver, design
	return nil
}

func (s *Service) resolveQuiz(ctx command.Context, uid uint64) (events.Schedule, error) {
	if s.miniContent != nil {
		typ, id, start, end, err := s.miniContent.ResolveMiniContentUID(ctx, uid)
		if err == nil {
			if typ != 13 {
				return events.Schedule{}, errors.New("eventactions: content UID is not a quiz")
			}
			return events.Schedule{UID: uid, ID: id, Start: start, End: end}, nil
		}
	}
	return s.registry.Resolve(uid)
}
