package calendar

import (
	"fmt"

	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
)

// validateMiniHubBindings rejects invalid published routes at startup instead
// of letting a client open an incompatible prefab or use another activity UID.
func (s *Set) validateMiniHubBindings(design *gamedata.EventPlayCatalog) error {
	uid := make(map[uint64]events.Schedule)
	for _, v := range s.Events {
		if v.UID != 0 {
			uid[v.UID] = v
		}
	}
	value := func(row []byte, field int) uint64 { v, _, _ := wire.Varint(row, field); return v }
	for _, hub := range s.EventHubs {
		table, err := design.Row("PackEventHubTable", 14, hub.HubID)
		if err != nil {
			return err
		}
		if value(table, 13) != 1 {
			continue
		}
		if hub.UID == 0 {
			return fmt.Errorf("calendar: mini hub %d requires a nonzero UID", hub.HubID)
		}
		start, err := timestamp(hub.Start)
		if err != nil {
			return err
		}
		playEnd, err := timestamp(hub.PlayEnd)
		if err != nil {
			return err
		}
		end, err := timestamp(hub.End)
		if err != nil {
			return err
		}
		for _, binding := range hub.Settings {
			var slot []byte
			for _, row := range design.Rows("PackEventListTable", 6, hub.HubID) {
				if value(row, 11) != binding.Slot {
					continue
				}
				if slot != nil {
					return fmt.Errorf("calendar: mini hub %d slot %d is ambiguous in GameData", hub.HubID, binding.Slot)
				}
				slot = row
			}
			if slot == nil {
				return fmt.Errorf("calendar: mini hub %d slot %d missing GameData", hub.HubID, binding.Slot)
			}
			contentType, contentID := value(slot, 9), value(slot, 7)
			if binding.ProgressType != contentType {
				return fmt.Errorf("calendar: mini hub %d slot %d content type %d, want %d", hub.HubID, binding.Slot, binding.ProgressType, contentType)
			}
			eventType, supported := gamedata.MiniHubEventType(contentType)
			// Mini stories and NPC quizzes have their own slot UID namespace,
			// distinct from Define_EventType (13 there means bingo). Their group
			// and availability derive from this static slot and the hub window.
			if contentType == 13 || contentType == 14 {
				if len(binding.EventUIDs) != 1 || binding.EventUIDs[0] == 0 {
					return fmt.Errorf("calendar: mini hub %d slot %d requires one local content UID", hub.HubID, binding.Slot)
				}
				if _, collision := uid[binding.EventUIDs[0]]; collision {
					return fmt.Errorf("calendar: mini hub %d slot %d content UID collides with global event", hub.HubID, binding.Slot)
				}
				if value(slot, 4) != 0 {
					return fmt.Errorf("calendar: mini hub %d story/quiz slot has invalid end type", hub.HubID)
				}
				continue
			}
			if !supported {
				return fmt.Errorf("calendar: mini hub %d slot %d content type %d has no supported scheduled route", hub.HubID, binding.Slot, contentType)
			}
			endType := value(slot, 4)
			if endType > 1 {
				return fmt.Errorf("calendar: mini hub %d slot %d unsupported end type %d", hub.HubID, binding.Slot, endType)
			}
			matches := 0
			for _, id := range binding.EventUIDs {
				child, ok := uid[id]
				if !ok || child.Type != eventType || child.ID != contentID || child.SubID != 0 {
					return fmt.Errorf("calendar: mini hub %d slot %d references incompatible event UID %d", hub.HubID, binding.Slot, id)
				}
				childStart, childEnd := child.Start, child.End
				if childStart < int64(start) {
					childStart = int64(start)
				}
				if childEnd > int64(end) {
					childEnd = int64(end)
				}
				// Project policy: EndDateType=0 closes the slot at PlayEnd;
				// EndDateType=1 allows it through the final hub End window.
				if endType == 0 && childEnd > int64(playEnd) {
					childEnd = int64(playEnd)
				}
				if childStart < childEnd {
					matches++
				}
			}
			if matches != 1 {
				return fmt.Errorf("calendar: mini hub %d slot %d has %d schedules in its window, want one", hub.HubID, binding.Slot, matches)
			}
		}
	}
	return nil
}
