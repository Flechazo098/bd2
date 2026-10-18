package roguelike

func (s *Service) systemEffect(r *run, typ uint64) float64 {
	if typ == 0 {
		return 0
	}
	var value float64
	for _, item := range r.Relics {
		for _, id := range s.design.Relics.Row(0, item.ID).A(3) {
			buff := s.design.Buffs.Row(0, id)
			if buff.V(6) == typ {
				value += buff.D(7)
			}
		}
	}
	return value
}
func bonusGoldType(room uint64) uint64 {
	switch room {
	case 1:
		return 4
	case 2:
		return 5
	}
	return 0
}
