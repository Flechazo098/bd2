package eventactions

import (
	"bd2server/internal/server/design/gamedata"

	"errors"
	"sort"
)

// BattlePlayLibrary gives TacticsBingo the standard 3 by 4 grid and five
// characters. Positions are zero-based; DeckSave sequences start at one.
const tacticsGridSize = 3 * 4
const tacticsPartySize = 5

type tacticsDeckEntry struct {
	index, character, costume, position, sequence uint64
}

// SetPlayDataTarosTactics builds the first five authored CharGroup rows with
// their CharTable default costumes; player ownership never enters this path.
func (s *Service) tacticsBlueParty(group uint64) (map[uint64]uint64, error) {
	var rows []gamedata.EventActionRow
	for _, row := range s.design.Tables["CharGroupTable"] {
		if group != 0 && row.V(2) == group {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].V(3) < rows[j].V(3) })
	if len(rows) > tacticsPartySize {
		rows = rows[:tacticsPartySize]
	}
	if len(rows) == 0 {
		return nil, errors.New("eventactions: tactics blue party missing")
	}
	party := map[uint64]uint64{}
	for _, row := range rows {
		id := row.V(1)
		character, ok := s.design.Row("CharTable", 12, id)
		if id == 0 || party[id] != 0 || row.V(5) == 0 || !ok || character.V(5) == 0 {
			return nil, errors.New("eventactions: invalid tactics blue party design")
		}
		party[id] = character.V(5)
	}
	return party, nil
}

func matchesTacticsParty(entries []tacticsDeckEntry, party map[uint64]uint64) bool {
	if len(entries) != len(party) {
		return false
	}
	for _, e := range entries {
		if party[e.character] != e.costume {
			return false
		}
	}
	return true
}
