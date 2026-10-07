package world

import (
	"bd2server/internal/server/domain/command"
	"fmt"
)

// ValidateQuestBattle checks ownership after GameData resolves the monster's
// quest range and battle deck. A side quest remains normal difficulty even
// when the player has selected a harder main story.
func (s *Service) ValidateQuestBattle(ctx command.Context, pack int, questIDs []uint64) error {
	current, err := s.CurrentPackID(ctx)
	if err != nil || current != pack || len(questIDs) == 0 {
		return fmt.Errorf("%w: quest battle pack", ErrInvalidRequest)
	}
	quests, known := s.questsFor(ctx, pack)
	if !known {
		return fmt.Errorf("%w: quest battle catalog", ErrInvalidRequest)
	}
	for _, id := range questIDs {
		if id == 0 || id > 0x7fffffff {
			continue
		}
		quest, exists := quests[int(id)]
		if !exists || s.state.QuestCleared(int(id), pack, s.questDifficultyFor(pack, int(id))) {
			continue
		}
		if quest.Type == 0 && s.firstUnclearedQuestFor(pack) == int(id) {
			return nil
		}
		if quest.Type == 1 {
			if _, accepted := s.state.QuestInPack(int(id), pack, 0); accepted {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: quest battle has no active quest", ErrInvalidRequest)
}
