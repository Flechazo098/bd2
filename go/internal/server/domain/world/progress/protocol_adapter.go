package progress

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
)

// UpdateQuest consumes QuestUpdateRequest: field 1 seq, field 2 quest_id,
// field 3 pack_id, and repeated packed/unpacked int32 field 4 quest_value.
// It returns the quest id that the response must echo as update_quest_id.
func (s *Store) UpdateQuest(ctx command.Context, request []byte) (int, error) {
	questID, found, err := wire.Varint(request, 2)
	if err != nil || !found || questID == 0 || questID > uint64(^uint32(0)>>1) {
		return 0, fmt.Errorf("%w: quest id", ErrInvalidQuest)
	}
	packID, found, err := wire.Varint(request, 3)
	if err != nil || !found || packID == 0 || packID > uint64(^uint32(0)>>1) {
		return 0, fmt.Errorf("%w: pack id", ErrInvalidQuest)
	}
	values := make([]int, 0, 4)
	if err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != 4 {
			return nil
		}
		switch field.Type {
		case 0:
			value, count := binary.Uvarint(field.Value)
			if count <= 0 || value > uint64(^uint32(0)>>1) {
				return ErrInvalidQuest
			}
			values = append(values, int(value))
		case 2:
			for remaining := field.Value; len(remaining) > 0; {
				value, count := binary.Uvarint(remaining)
				if count <= 0 || value > uint64(^uint32(0)>>1) {
					return ErrInvalidQuest
				}
				values = append(values, int(value))
				remaining = remaining[count:]
			}
		default:
			return ErrInvalidQuest
		}
		return nil
	}); err != nil {
		return 0, fmt.Errorf("%w: quest values", ErrInvalidQuest)
	}

	progress := QuestProgress{QuestID: int(questID), PackID: int(packID), Values: append([]int(nil), values...)}

	quests := make(map[string]QuestProgress, len(s.quests)+1)
	maps.Copy(quests, s.quests)
	selection := s.selections[strconv.Itoa(progress.PackID)]
	progress.Difficulty = selection.Difficulty
	if _, acceptedNormal := s.quests[questKey(progress.PackID, progress.QuestID)]; acceptedNormal && selection.QuestID != progress.QuestID {
		progress.Difficulty = 0
	}
	quests[questKey(progress.PackID, progress.QuestID, progress.Difficulty)] = progress
	if err := s.commit(ctx, s.position, s.tutorials, quests, s.cleared); err != nil {
		return 0, err
	}
	return progress.QuestID, nil
}

// SaveUserPosition consumes SaveUserPositionRequest:
// field 1 seq, field 2 pack_id, field 3 pack_position JSON.
func (s *Store) SaveUserPosition(ctx command.Context, request []byte) error {
	packID, found, err := wire.Varint(request, 2)
	if err != nil || !found || packID == 0 || packID > uint64(^uint32(0)>>1) {
		return fmt.Errorf("%w: pack id", ErrInvalidPosition)
	}
	raw, found, err := wire.Bytes(request, 3)
	if err != nil || !found || len(raw) == 0 || len(raw) > 64<<10 {
		return fmt.Errorf("%w: position JSON", ErrInvalidPosition)
	}
	var position Position
	if err := json.Unmarshal(raw, &position); err != nil || position.MapID <= 0 {
		return fmt.Errorf("%w: decode JSON", ErrInvalidPosition)
	}

	return s.commit(ctx, SavedPosition{Difficulty: s.selections[strconv.Itoa(int(packID))].Difficulty, PackID: int(packID), Position: position, RawJSON: string(raw)}, s.tutorials, s.quests, s.cleared)
}

// ClearTutorial consumes TutorialClearRequest: field 1 seq, field 2 id.
// Repeated requests are idempotent.
func (s *Store) ClearTutorial(ctx command.Context, request []byte) error {
	id, found, err := wire.Varint(request, 2)
	if err != nil || !found || id == 0 || id > uint64(^uint32(0)>>1) {
		return ErrInvalidTutorial
	}

	tutorials := make(map[int]struct{}, len(s.tutorials)+1)
	for cleared := range s.tutorials {
		tutorials[cleared] = struct{}{}
	}
	tutorials[int(id)] = struct{}{}
	return s.commit(ctx, s.position, tutorials, s.quests, s.cleared)
}

func (s *Store) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	switch path {
	case "/SaveUserPosition":
		if err := s.SaveUserPosition(ctx, request); err != nil {
			return 0, nil, true, fmt.Errorf("%s: %w", path, err)
		}
		if saved, found := s.Position(); found {
			slog.Info("field position saved", "pack", saved.PackID, "map", saved.Position.MapID)
		}
		return 7, nil, true, nil
	case "/TutorialClear":
		if err := s.ClearTutorial(ctx, request); err != nil {
			return 0, nil, true, fmt.Errorf("%s: %w", path, err)
		}
		return 102, nil, true, nil
	case "/TutorialInfo":
		if seq, found, err := wire.Varint(request, 1); err != nil || !found || seq == 0 {
			return 0, nil, true, ErrInvalidTutorial
		}
		var packed []byte
		for _, id := range s.Tutorials() {
			packed = binary.AppendUvarint(packed, uint64(id))
		}
		var response []byte
		if len(packed) > 0 {
			response = wire.AppendBytes(response, 1, packed)
		}
		return 101, response, true, nil
	default:
		return 0, nil, false, nil
	}
}

func (s *Store) SaveFieldBuff(ctx command.Context, id uint64, raw []byte) error {

	storedID, _, err := wire.Varint(raw, 1)
	if err != nil || id == 0 || id > 0x7fffffff || storedID != id {
		return fmt.Errorf("progress: invalid field buff identity")
	}
	store, err := s.fieldRewardEntries()
	if err != nil {
		return err
	}
	return store.PutEntry(ctx.State, "progress", "field_buffs", strconv.FormatUint(id, 10), raw)
}

func (s *Store) FieldBuffs(ctx command.Context) ([][]byte, error) {

	store, err := s.fieldRewardEntries()
	if err != nil {
		return nil, err
	}
	entries, err := store.ListEntries(ctx.State, "progress", "field_buffs")
	if err != nil {
		return nil, err
	}
	ids := make([]uint64, 0, len(entries))
	for key, raw := range entries {
		id, err := strconv.ParseUint(key, 10, 32)
		storedID, _, parseErr := wire.Varint(raw, 1)
		if err != nil || parseErr != nil || id == 0 || id > 0x7fffffff || id != storedID {
			return nil, fmt.Errorf("progress: invalid saved field buff")
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var out [][]byte
	for _, id := range ids {
		out = append(out, entries[strconv.FormatUint(id, 10)])
	}
	return out, nil
}
