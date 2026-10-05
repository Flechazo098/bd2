package eventactions

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
)

type MiniContentResolver interface {
	ResolveMiniContentUID(uint64) (uint64, uint64, int64, int64, error)
	ListMiniContentRoutes() ([]gamedata.MiniContentRoute, error)
}

func (s *Service) AttachMiniContent(resolver MiniContentResolver, design *gamedata.MiniContentDesign) error {
	if resolver == nil || design == nil {
		return errors.New("eventactions: mini content dependencies missing")
	}
	routes, err := resolver.ListMiniContentRoutes()
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

func (s *Service) resolveQuiz(uid uint64) (events.Schedule, error) {
	if s.miniContent != nil {
		typ, id, start, end, err := s.miniContent.ResolveMiniContentUID(uid)
		if err == nil {
			if typ != 13 {
				return events.Schedule{}, errors.New("eventactions: content UID is not a quiz")
			}
			return events.Schedule{UID: uid, ID: id, Start: start, End: end}, nil
		}
	}
	return s.registry.Resolve(uid)
}

func (s *Service) dailyStory(path string, b []byte, identity string) ([]byte, error) {
	if path == "/DailyStoryInfo" {
		var ids []uint64
		for k, claimed := range s.state.Claims {
			if claimed && strings.HasPrefix(k, "daily-story:") {
				id, err := strconv.ParseUint(strings.TrimPrefix(k, "daily-story:"), 10, 64)
				if err == nil {
					ids = append(ids, id)
				}
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		var out []byte
		for _, id := range ids {
			out = wire.AppendVarint(out, 1, id)
		}
		return out, nil
	}
	id := val(b, 2)
	if id == 0 || s.miniContent == nil || s.miniDesign == nil {
		return nil, errors.New("eventactions: daily story unavailable")
	}
	reward, ok := s.miniDesign.Stories[id]
	if !ok {
		return nil, errors.New("eventactions: unknown daily story")
	}
	ck := fmt.Sprintf("daily-story:%d", id)
	if s.state.Claims[ck] {
		return wire.AppendVarint(wire.AppendBytes(nil, 1, nil), 2, id), nil
	}
	routes, err := s.miniContent.ListMiniContentRoutes()
	if err != nil {
		return nil, err
	}
	available := false
	now := s.now().UnixMilli()
	for _, route := range routes {
		if route.ContentType != 14 || now < route.Start || now > route.End {
			continue
		}
		for _, story := range s.miniDesign.Groups[route.ContentID] {
			if story == id {
				available = true
			}
		}
	}
	if !available {
		return nil, errors.New("eventactions: daily story event inactive")
	}
	var rewards []gamedata.Reward
	if reward.Count > 0 {
		rewards = append(rewards, reward)
	}
	bundle, err := s.economy.Apply(identity, nil, rewards)
	if err != nil {
		return nil, err
	}
	s.state.Claims[ck] = true
	return wire.AppendVarint(wire.AppendBytes(nil, 1, bundle), 2, id), nil
}
