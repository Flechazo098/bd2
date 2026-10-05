package world

// CashPackagePackCleared verifies the complete main quest chain at the
// requested difficulty using persisted server progress.
func (s *Service) CashPackagePackCleared(packID, difficulty uint64) bool {
	if s.storyCatalog == nil || difficulty > 2 || packID == 0 {
		return false
	}
	pack, ok := s.storyCatalog.Packs[int(packID)]
	if !ok || len(pack.MainQuestIDs) == 0 {
		return false
	}
	for _, quest := range pack.MainQuestIDs {
		if !s.state.QuestCleared(quest, int(packID), int(difficulty)) {
			return false
		}
	}
	return true
}
