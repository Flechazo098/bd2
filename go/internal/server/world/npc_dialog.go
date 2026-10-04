package world

import "bd2server/internal/server/wire"

// handleQuestUpdate accepts the existing task update packet used when an NPC
// conversation finishes. Ordinary Talk is local and sends AchievementUpdate,
// not a separate NPC dialog packet. QuestUpdate has no NPC identity to validate.
func (s *Service) handleQuestUpdate(request []byte) (int, []byte, bool, error) {
	quest, pack, err := requestQuest(request)
	if err != nil {
		return 0, nil, true, err
	}
	current, err := s.CurrentPackID()
	if err != nil || current != pack || !s.canClear(pack, quest) {
		return 0, nil, true, ErrInvalidRequest
	}
	if s.state.QuestCleared(quest, pack, s.questDifficultyFor(pack, quest)) {
		// A delayed replay must not reinsert a cleared quest into active progress.
		return 19, wire.AppendBytes(wire.AppendVarint(nil, 1, uint64(quest)), 2, nil), true, nil
	}
	if _, err := s.state.UpdateQuest(request); err != nil {
		return 0, nil, true, err
	}
	// field 2 is RewardDBInfoBundle, not QuestDBInfo. Task completion/claims
	// remain in QuestClear; an update alone must not invent or duplicate rewards.
	return 19, wire.AppendBytes(wire.AppendVarint(nil, 1, uint64(quest)), 2, nil), true, nil
}
