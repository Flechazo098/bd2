package player

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"bd2server/internal/stateio"
	"bd2server/internal/versionconfig"
)

func loadCharacterEntries(store stateio.AtomicEntryStore, core []byte) (characterSnapshot, []Character, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(core, &fields); err != nil {
		return characterSnapshot{}, nil, fmt.Errorf("player: decode character core: %w", err)
	}
	if len(fields) != 2 || fields["version"] == nil || fields["character_order"] == nil {
		return characterSnapshot{}, nil, errors.New("player: invalid character core fields")
	}
	var saved characterSnapshot
	if err := json.Unmarshal(core, &saved); err != nil {
		return characterSnapshot{}, nil, fmt.Errorf("player: decode character core: %w", err)
	}
	if saved.Version != versionconfig.Protocol() || saved.CharacterOrder == nil {
		return characterSnapshot{}, nil, errors.New("player: invalid character save version or order")
	}
	rows, err := store.ListEntries("characters", "characters")
	if err != nil {
		return characterSnapshot{}, nil, fmt.Errorf("player: list character entries: %w", err)
	}
	if len(rows) != len(saved.CharacterOrder) {
		return characterSnapshot{}, nil, errors.New("player: character order and entries differ")
	}
	owned := make(map[uint64]Character, len(rows))
	for key, payload := range rows {
		index, err := strconv.ParseUint(key, 10, 64)
		if err != nil || index == 0 || key != strconv.FormatUint(index, 10) {
			return characterSnapshot{}, nil, fmt.Errorf("player: invalid character entry key %q", key)
		}
		var character Character
		if err := json.Unmarshal(payload, &character); err != nil {
			return characterSnapshot{}, nil, fmt.Errorf("player: decode character entry %q: %w", key, err)
		}
		if character.InvenIndex != index {
			return characterSnapshot{}, nil, fmt.Errorf("player: character entry %q has mismatched index", key)
		}
		owned[index] = character
	}
	characters := make([]Character, 0, len(saved.CharacterOrder))
	seen := make(map[uint64]bool, len(saved.CharacterOrder))
	for _, index := range saved.CharacterOrder {
		character, found := owned[index]
		if index == 0 || seen[index] || !found {
			return characterSnapshot{}, nil, fmt.Errorf("player: invalid character order index %d", index)
		}
		characters = append(characters, character)
		seen[index] = true
	}
	if err := validateCharacters(characters); err != nil {
		return characterSnapshot{}, nil, err
	}
	return saved, characters, nil
}

func (s *CharacterStore) persist(next []Character) error {
	if err := validateCharacters(next); err != nil {
		return err
	}
	previous := make(map[uint64]Character, len(s.characters))
	for _, character := range s.characters {
		previous[character.InvenIndex] = character
	}
	current := make(map[uint64]bool, len(next))
	order := make([]uint64, 0, len(next))
	changes := make([]stateio.EntryMutation, 0)
	for _, character := range next {
		index := character.InvenIndex
		current[index] = true
		order = append(order, index)
		if s.persisted[index] && reflect.DeepEqual(previous[index], character) {
			continue
		}
		payload, err := json.Marshal(character)
		if err != nil {
			return fmt.Errorf("player: encode character %d: %w", index, err)
		}
		changes = append(changes, stateio.EntryMutation{
			Bucket: "characters", Key: strconv.FormatUint(index, 10), Payload: payload,
		})
	}
	for index := range s.persisted {
		if !current[index] {
			changes = append(changes, stateio.EntryMutation{
				Bucket: "characters", Key: strconv.FormatUint(index, 10), Delete: true,
			})
		}
	}
	var core []byte
	if !s.persistedCore || !reflect.DeepEqual(s.persistedOrder, order) {
		var err error
		core, err = json.Marshal(characterSnapshot{Version: versionconfig.Protocol(), CharacterOrder: order})
		if err != nil {
			return fmt.Errorf("player: encode character core: %w", err)
		}
	}
	if core == nil && len(changes) == 0 {
		return nil
	}
	if err := s.store.SaveWithEntries("characters", core, changes); err != nil {
		return err
	}
	s.persisted = current
	s.persistedOrder = order
	s.persistedCore = true
	return nil
}
