package world

import (
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/protocol/wire"
	"fmt"
)

// Main-story replay: once every main quest of a pack is cleared on hard (2),
// QuestReplayPopupUI accepts the first main quest with is_retry, a difficulty
// 0-2 and the battle-skip toggle; QuestPopupUI continues the saved quest. The
// client hides the reward panel while replaying, so replay clears grant nothing.
const replayUnlockDifficulty = 2

// replayWire encodes QuestRetryDBInfo; the client converts it into the active
// QuestDBInfo, so quest values travel as object_id like questInfoWire.
func replayWire(pack int, replay progress.QuestReplay) []byte {
	out := wire.AppendVarint(nil, 1, uint64(pack))
	if replay.Difficulty != 0 {
		out = wire.AppendVarint(out, 2, uint64(replay.Difficulty))
	}
	out = wire.AppendVarint(out, 3, uint64(replay.QuestID))
	for _, value := range replay.Values {
		out = wire.AppendVarint(out, 5, uint64(value))
	}
	if replay.BattleSkip {
		out = wire.AppendVarint(out, 6, 1)
	}
	return out
}

// endedReplayWire has no quest id; QuestPacket.AddReplayQuest drops the
// client's replay entry for a zero QuestId.
func endedReplayWire(pack int) []byte { return wire.AppendVarint(nil, 1, uint64(pack)) }

func (s *Service) replayUnlocked(pack int) bool {
	ids := s.storyCatalog.Packs[pack].MainQuestIDs
	if len(ids) == 0 || !s.questDifficulties[pack][replayUnlockDifficulty] {
		return false
	}
	for _, id := range ids {
		if !s.state.QuestCleared(id, pack, replayUnlockDifficulty) {
			return false
		}
	}
	return true
}

func (s *Service) acceptQuestReplay(ctx command.Context, pack, quest int, request []byte) (int, []byte, bool, error) {
	level, _, err := wire.Varint(request, 4)
	if err != nil || level > replayUnlockDifficulty || level > 0 && !s.questDifficulties[pack][int(level)] {
		return 0, nil, true, fmt.Errorf("%w: replay difficulty", ErrInvalidRequest)
	}
	skip, _, err := wire.Varint(request, 7)
	if err != nil || skip > 1 || !s.replayUnlocked(pack) {
		return 0, nil, true, fmt.Errorf("%w: replay unavailable for pack %d", ErrInvalidRequest, pack)
	}
	replay, active, err := s.state.QuestReplay(ctx, pack)
	if err != nil {
		return 0, nil, true, err
	}
	switch {
	case active && quest == replay.QuestID && int(level) == replay.Difficulty:
	case quest == s.storyCatalog.Packs[pack].MainQuestIDs[0]:
		previous, _ := s.state.Selection(pack)
		if active {
			previous = replay.Previous
		}
		replay = progress.QuestReplay{QuestID: quest, Difficulty: int(level), Previous: previous}
	default:
		return 0, nil, true, fmt.Errorf("%w: replay quest %d", ErrInvalidRequest, quest)
	}
	replay.BattleSkip = skip == 1
	if err := s.state.SaveQuestReplay(ctx, pack, replay); err != nil {
		return 0, nil, true, err
	}
	// The selection carries the replay difficulty to battles and story parties.
	if err := s.state.SelectQuest(ctx, pack, progress.QuestSelection{QuestID: quest, Difficulty: replay.Difficulty}); err != nil {
		return 0, nil, true, err
	}
	if err := s.state.SetActivePackID(ctx, pack); err != nil {
		return 0, nil, true, err
	}
	s.setCurrentPack(pack)
	chars, decks, err := s.resolveActivePartyWires(ctx, pack, quest)
	if err != nil {
		return 0, nil, true, err
	}
	var out []byte
	for _, char := range chars {
		out = wire.AppendBytes(out, 2, char)
	}
	for _, deck := range decks {
		out = wire.AppendBytes(out, 3, deck)
	}
	items, err := s.ensureQuestItems(ctx, pack, quest)
	if err != nil {
		return 0, nil, true, err
	}
	for _, item := range items {
		out = wire.AppendBytes(out, 4, assets.ItemWire(item))
	}
	return 17, wire.AppendBytes(out, 5, replayWire(pack, replay)), true, nil
}

func (s *Service) endQuestReplay(ctx command.Context, pack int, replay progress.QuestReplay) error {
	if err := s.state.DeleteQuestReplay(ctx, pack); err != nil {
		return err
	}
	return s.state.SelectQuest(ctx, pack, replay.Previous)
}

func (s *Service) giveUpQuestReplay(ctx command.Context, pack, quest int, replay progress.QuestReplay) (int, []byte, bool, error) {
	if quest != replay.QuestID {
		return 0, nil, true, fmt.Errorf("%w: replay quest %d is not active", ErrInvalidRequest, quest)
	}
	if err := s.endQuestReplay(ctx, pack, replay); err != nil {
		return 0, nil, true, err
	}
	out := s.appendCurrentBattleDeck(wire.AppendVarint(nil, 1, uint64(quest)), 2)
	return 20, wire.AppendBytes(out, 5, endedReplayWire(pack)), true, nil
}

func (s *Service) updateQuestReplay(ctx command.Context, pack int, replay progress.QuestReplay, request []byte) error {
	values, err := progress.QuestValues(request)
	if err != nil {
		return err
	}
	replay.Values = values
	return s.state.SaveQuestReplay(ctx, pack, replay)
}

func (s *Service) clearQuestReplay(ctx command.Context, pack, quest int, replay progress.QuestReplay) (int, []byte, bool, error) {
	if quest != replay.QuestID {
		return 0, nil, true, fmt.Errorf("%w: replay quest %d is not active", ErrInvalidRequest, quest)
	}
	var previousParty []roster.Character
	var err error
	if s.storyRoster != nil {
		if previousParty, err = s.resolveStoryCharacters(ctx, pack, quest); err != nil {
			return 0, nil, true, err
		}
	}
	var nextItems []assets.Item
	var nextChars [][]byte
	retry := endedReplayWire(pack)
	if next := s.nextQuestFor(ctx, pack, quest); next != 0 {
		replay.QuestID, replay.Values = next, nil
		if err = s.state.SaveQuestReplay(ctx, pack, replay); err != nil {
			return 0, nil, true, err
		}
		if err = s.state.SelectQuest(ctx, pack, progress.QuestSelection{QuestID: next, Difficulty: replay.Difficulty}); err != nil {
			return 0, nil, true, err
		}
		if nextChars, _, err = s.resolveActivePartyWires(ctx, pack, next); err != nil {
			return 0, nil, true, err
		}
		if nextChars, err = storyPartyChanges(previousParty, nextChars); err != nil {
			return 0, nil, true, err
		}
		if nextItems, err = s.ensureQuestItems(ctx, pack, next); err != nil {
			return 0, nil, true, err
		}
		retry = replayWire(pack, replay)
	} else if err = s.endQuestReplay(ctx, pack, replay); err != nil {
		return 0, nil, true, err
	}
	out, err := s.clearResponse(ctx, pack, quest, nil, nextItems, nextChars)
	if err != nil {
		return 0, nil, true, err
	}
	return 18, wire.AppendBytes(out, 16, retry), true, nil
}

// saveQuestReplayBattleSkip consumes QuestRetryBattleSkipSaveRequest: field 1
// seq, field 2 pack_id, field 3 is_battle_skip.
func (s *Service) saveQuestReplayBattleSkip(ctx command.Context, request []byte) (int, []byte, bool, error) {
	pack, err := requestPack(request)
	if err != nil {
		return 0, nil, true, err
	}
	skip, _, err := wire.Varint(request, 3)
	if err != nil || skip > 1 {
		return 0, nil, true, fmt.Errorf("%w: battle skip flag", ErrInvalidRequest)
	}
	replay, active, err := s.state.QuestReplay(ctx, pack)
	if err != nil {
		return 0, nil, true, err
	}
	if !active {
		return 0, nil, true, fmt.Errorf("%w: pack %d is not replaying", ErrInvalidRequest, pack)
	}
	replay.BattleSkip = skip == 1
	if err := s.state.SaveQuestReplay(ctx, pack, replay); err != nil {
		return 0, nil, true, err
	}
	var out []byte
	if replay.BattleSkip {
		out = wire.AppendVarint(out, 1, 1)
	}
	return 654, out, true, nil
}
