package world

import "math"

// MissionsUnlocked uses the same pack authorization as map entry. A quest
// condition requires a committed normal-difficulty clear in that pack.
func (s *Service) MissionsUnlocked(pack, quest uint64) bool {
	if pack == 0 {
		return quest == 0
	}
	if pack > math.MaxInt32 || quest > math.MaxInt32 {
		return false
	}
	if quest == 0 {
		return s.packUnlocked(int(pack))
	}
	return s.state.QuestCleared(int(quest), int(pack), 0)
}
