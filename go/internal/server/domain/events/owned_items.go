package events

func (e *Economy) AttachOwnedItemDesign(d map[uint64]map[uint64]bool) { e.ownedDesign = d }
