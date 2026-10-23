package progress

import (
	"bd2server/internal/server/domain/command"
	"encoding/json"
	"fmt"
	"strconv"
)

// QuestReplay is a main-story replay of a pack whose hardest difficulty is
// complete. Previous is the selection restored when the replay ends.
type QuestReplay struct {
	QuestID    int            `json:"quest_id"`
	Difficulty int            `json:"difficulty"`
	Values     []int          `json:"values"`
	BattleSkip bool           `json:"battle_skip"`
	Previous   QuestSelection `json:"previous"`
}

func (r QuestReplay) valid() bool {
	return r.QuestID > 0 && r.Difficulty >= 0 && r.Difficulty <= 4 && r.Previous.QuestID >= 0 && r.Previous.Difficulty >= 0 && r.Previous.Difficulty <= 4 && r.Previous.Option >= 0
}

func (s *Store) QuestReplay(ctx command.Context, pack int) (QuestReplay, bool, error) {
	store, err := s.fieldRewardEntries()
	if err != nil {
		return QuestReplay{}, false, err
	}
	raw, found, err := store.LoadEntry(ctx.State, "progress", "quest_replays", strconv.Itoa(pack))
	if err != nil || !found {
		return QuestReplay{}, false, err
	}
	var replay QuestReplay
	if err = json.Unmarshal(raw, &replay); err != nil || !replay.valid() {
		return QuestReplay{}, false, fmt.Errorf("progress: invalid quest replay for pack %d", pack)
	}
	return replay, true, nil
}

func (s *Store) QuestReplays(ctx command.Context) (map[int]QuestReplay, error) {
	store, err := s.fieldRewardEntries()
	if err != nil {
		return nil, err
	}
	entries, err := store.ListEntries(ctx.State, "progress", "quest_replays")
	if err != nil {
		return nil, err
	}
	out := make(map[int]QuestReplay, len(entries))
	for key, raw := range entries {
		pack, err := strconv.Atoi(key)
		var replay QuestReplay
		if err != nil || pack <= 0 || json.Unmarshal(raw, &replay) != nil || !replay.valid() {
			return nil, fmt.Errorf("progress: invalid quest replay %q", key)
		}
		out[pack] = replay
	}
	return out, nil
}

func (s *Store) SaveQuestReplay(ctx command.Context, pack int, replay QuestReplay) error {
	if pack <= 0 || !replay.valid() {
		return fmt.Errorf("progress: invalid quest replay for pack %d", pack)
	}
	store, err := s.fieldRewardEntries()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(replay)
	if err != nil {
		return err
	}
	return store.PutEntry(ctx.State, "progress", "quest_replays", strconv.Itoa(pack), raw)
}

func (s *Store) DeleteQuestReplay(ctx command.Context, pack int) error {
	store, err := s.fieldRewardEntries()
	if err != nil {
		return err
	}
	_, err = store.DeleteEntry(ctx.State, "progress", "quest_replays", strconv.Itoa(pack))
	return err
}
