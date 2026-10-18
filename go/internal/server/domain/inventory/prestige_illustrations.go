package inventory

func (s *Inventory) AttachPrestigeIllustrations(illustrations map[uint64]uint64) {
	s.prestigeIllustrations = illustrations
}

func (s *Inventory) withPrestigeIllustrations(items []Item) []Item {
	if len(s.prestigeIllustrations) == 0 {
		return items
	}
	seen := make(map[uint64]bool)
	for _, item := range items {
		if item.Type == 25 && item.Count != 0 {
			seen[item.ID] = true
		}
	}
	var attached []Item
	for _, item := range items {
		if item.Type != 45 || item.Count == 0 {
			continue
		}
		id := s.prestigeIllustrations[item.ID]
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		// Linked illustrations are immutable ownership views, outside mutable item indices.
		attached = append(attached, Item{InvenIndex: 1<<62 | id, ID: id, Type: 25, Count: 1, TimeValue: item.TimeValue})
	}
	return append(items, attached...)
}
