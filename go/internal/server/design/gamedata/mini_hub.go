package gamedata

// MiniHubEventType maps PackEventListTable.HubContentType to the independent
// EventSchedule enum used by playable slot handlers. Quiz and daily story have
// no global event type and cannot be bound by borrowing another game's UID.
func MiniHubEventType(contentType uint64) (uint64, bool) {
	switch contentType {
	case 4:
		return 7, true
	case 5:
		return 12, true
	case 6:
		return 11, true
	case 7:
		return 13, true
	case 11:
		return 17, true
	case 12:
		return 19, true
	default:
		return 0, false
	}
}
