package eventactions

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"errors"
	"math"
	"sort"
)

// BattlePlayLibrary gives TacticsBingo the standard 3 by 4 grid and five
// characters. Positions are zero-based; DeckSave sequences start at one.
const tacticsGridSize = 3 * 4
const tacticsPartySize = 5

type tacticsDeckEntry struct {
	index, character, costume, position, sequence uint64
}

func decodeTacticsDeck(request []byte) ([]tacticsDeckEntry, error) {
	var entries []tacticsDeckEntry
	indices, characters, positions, sequences := map[uint64]bool{}, map[uint64]bool{}, map[uint64]bool{}, map[uint64]bool{}
	err := wire.Walk(request, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 2 {
			return errors.New("eventactions: malformed tactics deck entry")
		}
		seen := map[int]bool{}
		if err := wire.Walk(f.Value, func(v wire.Field) error {
			if v.Number >= 1 && v.Number <= 5 {
				if v.Type != 0 || seen[v.Number] {
					return errors.New("eventactions: malformed tactics deck field")
				}
				seen[v.Number] = true
			}
			return nil
		}); err != nil {
			return err
		}
		e := tacticsDeckEntry{val(f.Value, 1), val(f.Value, 2), val(f.Value, 3), val(f.Value, 4), val(f.Value, 5)}
		// These indices are client-generated virtual identities, not owned
		// inventory. Still enforce their signed protocol range and uniqueness.
		if e.index == 0 || e.index > math.MaxInt64 || e.character == 0 || e.character > math.MaxInt32 || e.costume == 0 || e.costume > math.MaxInt32 || e.position >= tacticsGridSize || e.sequence == 0 || e.sequence > tacticsPartySize || indices[e.index] || characters[e.character] || positions[e.position] || sequences[e.sequence] {
			return errors.New("eventactions: invalid tactics deck")
		}
		indices[e.index], characters[e.character], positions[e.position], sequences[e.sequence] = true, true, true, true
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 || len(entries) > tacticsPartySize {
		return nil, errors.New("eventactions: invalid tactics deck size")
	}
	for sequence := uint64(1); sequence <= uint64(len(entries)); sequence++ {
		if !sequences[sequence] {
			return nil, errors.New("eventactions: invalid tactics deck sequence")
		}
	}
	return entries, nil
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

func (s *Service) validateTacticsDeck(request []byte) error {
	entries, err := decodeTacticsDeck(request)
	if err != nil {
		return err
	}
	// The request only carries seq and deck_info. The client normally enters
	// the stage before saving; bind the saved party to that server context.
	if s.state.BattleUID != 0 {
		event, err := s.resolve(s.state.BattleUID, 20)
		if err != nil {
			return err
		}
		group, ok := s.design.Row("TacticsBingoGroupTable", 3, event.ID)
		stage, found := s.row("TacticsBingoTable", 4, 5, group.V(1), s.state.BattleStage)
		if !ok || !found || stage.V(2) != s.state.BattleDeck {
			return errors.New("eventactions: tactics battle context missing")
		}
		party, err := s.tacticsBlueParty(stage.V(1))
		if err != nil {
			return err
		}
		if matchesTacticsParty(entries, party) {
			return nil
		}
		return errors.New("eventactions: tactics deck does not match entered stage")
	}
	// Without a bound stage, accept only a complete authored party belonging
	// to a currently active tactics schedule, never an arbitrary mixed roster.
	for _, event := range s.registry.List() {
		if event.Type != 20 || !s.active(event) {
			continue
		}
		group, ok := s.design.Row("TacticsBingoGroupTable", 3, event.ID)
		if !ok {
			continue
		}
		for _, stage := range s.design.Tables["TacticsBingoTable"] {
			if stage.V(4) != group.V(1) {
				continue
			}
			party, err := s.tacticsBlueParty(stage.V(1))
			if err != nil {
				return err
			}
			if matchesTacticsParty(entries, party) {
				return nil
			}
		}
	}
	return errors.New("eventactions: tactics deck has no active authored party")
}
