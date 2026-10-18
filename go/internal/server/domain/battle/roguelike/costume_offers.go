package roguelike

func (s *Service) costumePool(r *run, initial bool) []uint64 {
	var pool []uint64
	for _, id := range s.design.CostumeIDs {
		design := s.design.Costumes[id]
		if s.options.CostumeVisible != nil && !s.options.CostumeVisible(id) {
			continue
		}
		if design.RequiresOwnership && (s.options.CostumeOwned == nil || !s.options.CostumeOwned(id)) {
			continue
		}
		eligible := true
		for _, c := range r.Costumes {
			if c.ID == id && (initial || c.Level >= design.MaxLevel) {
				eligible = false
				break
			}
		}
		if eligible {
			pool = append(pool, id)
		}
	}
	return pool
}

func (s *Service) pickCostume(r *run, pool []uint64, option uint64, initial bool) (uint64, []uint64, bool) {
	if len(pool) == 0 {
		return 0, pool, false
	}
	var preferred []int
	for i, id := range pool {
		owned := false
		for _, c := range r.Costumes {
			if c.ID == id {
				owned = true
				break
			}
		}
		if initial || (option == 1 && owned) || (option == 2 && !owned) {
			preferred = append(preferred, i)
		}
	}
	var at int
	if len(preferred) > 0 {
		at = preferred[s.sample(uint64(len(preferred)))]
	} else {
		at = int(s.sample(uint64(len(pool))))
	}
	id := pool[at]
	return id, append(pool[:at], pool[at+1:]...), true
}
