package battle

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"fmt"
	"slices"
)

func questVictoryKey(pack int, quest, difficulty uint64) string {
	return fmt.Sprintf("%d:%d:%d", pack, difficulty, quest)
}

func (s *Service) HasWonQuest(ctx command.Context, pack, quest, difficulty int) (bool, error) {
	if ctx.State == nil || pack <= 0 || quest <= 0 || difficulty < 0 || difficulty > 2 {
		return false, nil
	}
	receipt, found, err := ctx.State.LoadEntry("battle", "quest_victory", questVictoryKey(pack, uint64(quest), uint64(difficulty)))
	return found && len(receipt) != 0, err
}

func (s *Service) recordQuestVictory(ctx command.Context, state *battleState) error {
	if ctx.State == nil || state.Mode != 1 || state.Quest == 0 || state.Pack <= 0 {
		return nil
	}
	if s.currentDifficulty == nil {
		return fmt.Errorf("battle: quest victory difficulty unavailable")
	}
	difficulty, err := s.currentDifficulty(ctx)
	if err != nil {
		return err
	}
	loader := s.loadDifficultyDeck
	if loader == nil {
		loader = gamedata.ResolveQuestBattleDeck
	}
	enteredDeck := value(state.EntryResponse, 2)
	if enteredDeck == 0 {
		return fmt.Errorf("battle: quest victory entry deck unavailable")
	}
	resolved, err := loader(s.gameDataRoot, s.gameDataVersion, state.Pack, state.Monster, enteredDeck, difficulty)
	if err != nil {
		return fmt.Errorf("battle: verify quest victory difficulty: %w", err)
	}
	if resolved.DeckID != enteredDeck || !slices.Contains(resolved.QuestIDs, state.Quest) || state.EnterReceipt == "" {
		return fmt.Errorf("battle: quest victory differs from entered encounter")
	}
	return ctx.State.PutEntry("battle", "quest_victory", questVictoryKey(state.Pack, state.Quest, difficulty), []byte(state.EnterReceipt))
}
