package progress

import (
	"bd2server/internal/server/domain/command"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// FieldActionPosition stores the dropped position and quest state at that time.
// It is read from the transaction on demand, avoiding stale caches on rollback.
type FieldActionPosition struct {
	Position     []byte `json:"position"`
	QuestCleared bool   `json:"quest_cleared"`
}

func (s *Store) SaveFieldActionPosition(ctx command.Context, pack, id int, position FieldActionPosition) error {

	if pack <= 0 || id <= 0 || len(position.Position) == 0 {
		return fmt.Errorf("progress: invalid field action position")
	}
	store, err := s.fieldRewardEntries()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(position)
	if err != nil {
		return err
	}
	return store.PutEntry(ctx.State, "progress", "field_action_positions", fieldRewardKey(pack, id), raw)
}

func (s *Store) FieldActionPositions(ctx command.Context, pack int) (map[int]FieldActionPosition, error) {

	store, err := s.fieldRewardEntries()
	if err != nil {
		return nil, err
	}
	entries, err := store.ListEntries(ctx.State, "progress", "field_action_positions")
	if err != nil {
		return nil, err
	}
	out := map[int]FieldActionPosition{}
	prefix := fmt.Sprintf("%d:", pack)
	for key, raw := range entries {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		id, err := strconv.Atoi(strings.TrimPrefix(key, prefix))
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("progress: invalid field action key")
		}
		var value FieldActionPosition
		if err = json.Unmarshal(raw, &value); err != nil || len(value.Position) == 0 {
			return nil, fmt.Errorf("progress: invalid field action position")
		}
		out[id] = value
	}
	return out, nil
}
