package eventplay

import "fmt"

// FieldObjectEventPeriod follows EventLostCoinInfo: the exact public calendar
// must be active, and the installed event must explicitly contain this pack.
func (s *Service) FieldObjectEventPeriod(pack int) (string, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UnixMilli()
	period := ""
	var end int64
	for _, schedule := range s.registry.List() {
		if schedule.Type != 14 || now < schedule.Start || now >= schedule.End {
			continue
		}
		row, err := s.design.Row("EventLostCoinTable", 4, schedule.ID)
		if err != nil {
			return "", 0, err
		}
		packs, err := list(row, 1)
		if err != nil {
			return "", 0, err
		}
		matches := false
		for _, id := range packs {
			matches = matches || id == uint64(pack)
		}
		if !matches {
			continue
		}
		if period != "" {
			return "", 0, fmt.Errorf("eventplay: ambiguous active lost coin calendar for pack %d", pack)
		}
		period = fmt.Sprintf("event:%d:%d:%d:%d", schedule.UID, schedule.ID, schedule.Start, schedule.End)
		end = schedule.End
	}
	if period == "" {
		return "", 0, fmt.Errorf("eventplay: no active lost coin calendar for pack %d", pack)
	}
	return period, end, nil
}
