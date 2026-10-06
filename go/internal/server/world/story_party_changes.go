package world

import (
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
)

// QuestClear.CharInfo is processed by AddCharDBInfoReward and the join UI,
// not as a complete roster snapshot. DeckInfo still carries the full formation;
// only joining characters or authored level/costume changes belong in CharInfo.
func storyPartyChanges(previous []player.Character, next [][]byte) ([][]byte, error) {
	type appearance struct{ id, level, costume, useCostume uint64 }
	known := make(map[uint64]appearance, len(previous))
	for _, c := range previous {
		known[c.InvenIndex] = appearance{c.ID, c.Level, c.CostumeID, c.UseCostume}
	}
	var changed [][]byte
	for _, body := range next {
		var index uint64
		var value appearance
		for field, destination := range map[int]*uint64{1: &index, 2: &value.id, 4: &value.level, 5: &value.costume, 7: &value.useCostume} {
			var err error
			*destination, _, err = wire.Varint(body, field)
			if err != nil {
				return nil, err
			}
		}
		if old, exists := known[index]; exists && old == value {
			continue
		}
		changed = append(changed, body)
		known[index] = value
	}
	return changed, nil
}
