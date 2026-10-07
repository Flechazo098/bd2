package eventplay

import (
	"bd2server/internal/server/design/gamedata"
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
