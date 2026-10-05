package eventplay

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"fmt"
	"sort"
)

// miniHubSchedule is a project calendar identity; HubID is distinct from the
// pack ID used by EventPack schedules.
type miniHubBinding struct {
	Slot, ContentType uint64
	UIDs              []uint64
}

type miniHubSchedule struct {
	UID, HubID          uint64
	Start, PlayEnd, End int64
	Bindings            []miniHubBinding
}

// ListMiniContentRoutes exposes the same validated hub-scoped calendar used
// for presentation, including routes whose play period has already ended.
func (s *Service) ListMiniContentRoutes() ([]gamedata.MiniContentRoute, error) {
	body, err := s.miniEventHubs(wire.AppendVarint(nil, 1, 1))
	if err != nil {
		return nil, err
	}
	var routes []gamedata.MiniContentRoute
	seen := map[uint64]bool{}
	err = wire.Walk(body, func(hub wire.Field) error {
		if hub.Number != 1 || hub.Type != 2 {
			return nil
		}
		return wire.Walk(hub.Value, func(slot wire.Field) error {
			if slot.Number != 6 || slot.Type != 2 {
				return nil
			}
			typ := num(slot.Value, 2)
			if typ != 13 && typ != 14 {
				return nil
			}
			uid := num(slot.Value, 4)
			if seen[uid] {
				return fmt.Errorf("eventplay: ambiguous mini content UID %d", uid)
			}
			seen[uid] = true
			routes = append(routes, gamedata.MiniContentRoute{UID: uid, ContentType: typ, ContentID: num(slot.Value, 3), Start: int64(num(slot.Value, 5)), End: int64(num(slot.Value, 6))})
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].UID < routes[j].UID })
	return routes, nil
}

// ResolveMiniContentUID resolves hub-scoped story/quiz identities without
// manufacturing EventScheduleInfo rows. Callers enforce the returned window.
func (s *Service) ResolveMiniContentUID(uid uint64) (contentType, contentID uint64, start, end int64, err error) {
	if uid == 0 {
		return 0, 0, 0, 0, fmt.Errorf("eventplay: missing mini content UID")
	}
	body, err := s.miniEventHubs(wire.AppendVarint(nil, 1, 1))
	if err != nil {
		return 0, 0, 0, 0, err
	}
	found := false
	err = wire.Walk(body, func(hub wire.Field) error {
		if hub.Number != 1 || hub.Type != 2 {
			return nil
		}
		return wire.Walk(hub.Value, func(slot wire.Field) error {
			if slot.Number != 6 || slot.Type != 2 || num(slot.Value, 4) != uid {
				return nil
			}
			typ := num(slot.Value, 2)
			if typ != 13 && typ != 14 {
				return nil
			}
			if found {
				return fmt.Errorf("eventplay: ambiguous mini content UID %d", uid)
			}
			found = true
			contentType, contentID = typ, num(slot.Value, 3)
			start, end = int64(num(slot.Value, 5)), int64(num(slot.Value, 6))
			return nil
		})
	})
	if err != nil {
		return 0, 0, 0, 0, err
	}
	if !found {
		return 0, 0, 0, 0, fmt.Errorf("eventplay: mini content UID %d unavailable", uid)
	}
	return contentType, contentID, start, end, nil
}

func (s *Service) miniEventHubs(req []byte) ([]byte, error) {
	var hubs []miniHubSchedule
	if s.hubCalendars == nil {
		return nil, nil
	}
	_, body, handled, err := s.hubCalendars.Handle("/EventHubInfo", req)
	if err != nil {
		return nil, err
	}
	if handled {
		err = wire.Walk(body, func(f wire.Field) error {
			if f.Number != 1 || f.Type != 2 {
				return nil
			}
			hub := miniHubSchedule{UID: num(f.Value, 1), HubID: num(f.Value, 2), Start: int64(num(f.Value, 3)), PlayEnd: int64(num(f.Value, 4)), End: int64(num(f.Value, 5))}
			if err := wire.Walk(f.Value, func(setting wire.Field) error {
				if setting.Number != 6 || setting.Type != 2 {
					return nil
				}
				uids, err := list(setting.Value, 3)
				if err != nil {
					return err
				}
				hub.Bindings = append(hub.Bindings, miniHubBinding{num(setting.Value, 1), num(setting.Value, 2), uids})
				return nil
			}); err != nil {
				return err
			}
			hubs = append(hubs, hub)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(hubs, func(i, j int) bool { return hubs[i].UID < hubs[j].UID })
	rows := s.registry.List()
	var out []byte
	for _, hub := range hubs {
		table, err := s.design.Row("PackEventHubTable", 14, hub.HubID)
		if err != nil || num(table, 13) != 1 {
			continue
		}
		v := wire.AppendVarint(nil, 1, hub.UID)
		v = wire.AppendVarint(v, 2, hub.HubID)
		v = wire.AppendVarint(v, 3, uint64(hub.Start))
		v = wire.AppendVarint(v, 4, uint64(hub.PlayEnd))
		v = wire.AppendVarint(v, 5, uint64(hub.End))
		slots := s.design.Rows("PackEventListTable", 6, hub.HubID)
		sort.Slice(slots, func(i, j int) bool { return num(slots[i], 10) < num(slots[j], 10) })
		for _, binding := range hub.Bindings {
			found := false
			for _, slot := range slots {
				if binding.Slot == num(slot, 11) {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("eventplay: mini hub %d static slot index %d missing", hub.UID, binding.Slot)
			}
		}
		for _, slot := range slots {
			contentType, contentID := num(slot, 9), num(slot, 7)
			bound := map[uint64]bool{}
			for _, binding := range hub.Bindings {
				if binding.Slot == num(slot, 11) {
					if binding.ContentType != contentType {
						return nil, fmt.Errorf("eventplay: mini hub %d slot %d content type mismatch", hub.UID, num(slot, 10))
					}
					for _, uid := range binding.UIDs {
						bound[uid] = true
					}
				}
			}
			if len(bound) == 0 {
				continue
			}
			// Mini hub stories and NPC quizzes have hub-scoped UIDs, rather
			// than entries in EventScheduleInfo. Their explicit binding and
			// static EndDateType supply the calendar; never invent a global
			// event type for these separate client protocols.
			if contentType == 13 || contentType == 14 {
				if len(bound) != 1 || bound[0] {
					return nil, fmt.Errorf("eventplay: mini hub %d slot %d requires one nonzero hub-scoped UID", hub.UID, num(slot, 10))
				}
				var uid uint64
				for id := range bound {
					uid = id
				}
				for _, row := range rows {
					if row.UID == uid {
						return nil, fmt.Errorf("eventplay: mini hub %d slot %d hub-scoped UID collides with global schedule", hub.UID, num(slot, 10))
					}
				}
				end := hub.End
				if num(slot, 4) == 0 {
					end = hub.PlayEnd
				}
				if end <= hub.Start {
					continue
				}
				b := wire.AppendVarint(nil, 1, num(slot, 10))
				b = wire.AppendVarint(b, 2, contentType)
				b = wire.AppendVarint(b, 3, contentID)
				b = wire.AppendVarint(b, 4, uid)
				b = wire.AppendVarint(b, 5, uint64(hub.Start))
				b = wire.AppendVarint(b, 6, uint64(end))
				v = wire.AppendBytes(v, 6, b)
				continue
			}
			eventType, subID, known := s.miniSlotSchedule(contentType, contentID)
			if !known {
				return nil, fmt.Errorf("eventplay: mini hub %d slot %d content type %d unsupported", hub.UID, num(slot, 10), contentType)
			}
			for uid := range bound {
				found := false
				for _, row := range rows {
					if row.UID == uid && row.Type == eventType && row.ID == contentID && row.SubID == subID {
						found = true
						break
					}
				}
				if !found {
					return nil, fmt.Errorf("eventplay: mini hub %d slot %d schedule %d identity mismatch", hub.UID, num(slot, 10), uid)
				}
			}
			var matches []events.Schedule
			for _, row := range rows {
				if bound[row.UID] && row.Type == eventType && row.ID == contentID && row.SubID == subID && row.UID != 0 && row.Start < hub.End && hub.Start < row.End {
					matches = append(matches, row)
				}
			}
			if len(matches) == 0 {
				continue
			}
			if len(matches) > 1 {
				return nil, fmt.Errorf("eventplay: mini hub %d slot %d has ambiguous schedules", hub.UID, num(slot, 10))
			}
			child := matches[0]
			start, end := child.Start, child.End
			if start < hub.Start {
				start = hub.Start
			}
			if end > hub.End {
				end = hub.End
			}
			// Project policy follows static EndDateType: play content closes at
			// PlayEnd; exchange content can remain through the declared End.
			if num(slot, 4) == 0 && end > hub.PlayEnd {
				end = hub.PlayEnd
			}
			if end <= start {
				continue
			}
			b := wire.AppendVarint(nil, 1, num(slot, 10))
			b = wire.AppendVarint(b, 2, contentType)
			b = wire.AppendVarint(b, 3, contentID)
			b = wire.AppendVarint(b, 4, child.UID)
			b = wire.AppendVarint(b, 5, uint64(start))
			b = wire.AppendVarint(b, 6, uint64(end))
			v = wire.AppendBytes(v, 6, b)
		}
		out = wire.AppendBytes(out, 1, v)
	}
	return out, nil
}

// Hub content and EventType are separate client enums. MiniGame schedules
// are design identities; calendar SubID is zero for the supported slot routes.
func (s *Service) miniSlotSchedule(contentType, id uint64) (uint64, uint64, bool) {
	eventType, known := gamedata.MiniHubEventType(contentType)
	if !known {
		return 0, 0, false
	}
	if contentType == 6 {
		if _, err := s.design.Row("PackEventMiniGameTable", 8, id); err != nil {
			return 0, 0, false
		}
	}
	return eventType, 0, true
}

// Public event packs retain their declared wire settings and both end windows.
// Mini hub prefabs are served exclusively by MiniEventHubInfo.
func (s *Service) publicEventHubs(req []byte) ([]byte, error) {
	_, body, _, err := s.hubCalendars.Handle("/EventHubInfo", req)
	if err != nil {
		return nil, err
	}
	var out []byte
	err = wire.Walk(body, func(f wire.Field) error {
		if f.Number != 1 || f.Type != 2 {
			return nil
		}
		row, err := s.design.Row("PackEventHubTable", 14, num(f.Value, 2))
		if err != nil {
			return nil
		}
		if num(row, 13) == 0 {
			out = wire.AppendBytes(out, 1, f.Value)
		}
		return nil
	})
	return out, err
}
