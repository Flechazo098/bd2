package castle

import "bd2server/internal/server/domain/command"

// CashPackageTowerCleared follows the client's contiguous cleared-floor count.
func (s *Service) CashPackageTowerCleared(ctx command.Context, gate, floor uint64) (bool, error) {
	var mode uint64
	switch gate {
	case 2:
		mode = 16
	case 3:
		mode = 19
	case 4:
		mode = 20
	default:
		return false, nil
	}
	if floor == 0 {
		return false, nil
	}
	if _, ok := s.design.Towers[[2]uint64{mode, floor}]; !ok {
		return false, nil
	}
	v, _, _, err := s.load(ctx)
	if err != nil {
		return false, err
	}
	for id := uint64(1); id <= floor; id++ {
		if _, ok := v.Towers[towerKey(mode, id)]; !ok {
			return false, nil
		}
	}
	return true, nil
}
