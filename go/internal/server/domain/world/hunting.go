package world

import (
	"bd2server/internal/server/domain/command"
	"fmt"
)

func (s *Service) AttachHuntingGround(provider interface {
	EnsureForPack(ctx command.Context, _ int) ([]byte, error)
}) error {
	if provider == nil {
		return fmt.Errorf("world: missing hunting ground provider")
	}
	s.huntingGround = provider
	return nil
}

// HuntingEligibility follows the hunting tutorial: hard/extreme grounds need
// completion of the same story difficulty, independently of the last hunt boss.
func (s *Service) HuntingEligibility(ctx command.Context, pack int, difficulty uint64) error {
	if difficulty > 2 || !s.packUnlocked(ctx, pack) {
		return fmt.Errorf("world: hunting pack or difficulty locked")
	}
	if difficulty == 0 {
		return nil
	}
	if s.storyCatalog == nil {
		return fmt.Errorf("world: hunting story design unavailable")
	}
	d, ok := s.storyCatalog.Packs[pack]
	if !ok || len(d.MainQuestIDs) == 0 || !s.questDifficulties[pack][int(difficulty)] {
		return fmt.Errorf("world: hunting difficulty unavailable")
	}
	for _, id := range d.MainQuestIDs {
		if !s.state.QuestCleared(id, pack, int(difficulty)) {
			return fmt.Errorf("world: hunting requires completed story difficulty")
		}
	}
	return nil
}
