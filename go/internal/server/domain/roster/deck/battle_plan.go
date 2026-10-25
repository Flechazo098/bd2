package deck

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// BattleAction uses the native battle selection sentinels: zero is a normal
// attack, -1 is knock-back, and a positive value is an owned costume instance.
type BattleAction struct {
	CharacterIndex uint64 `json:"character_index"`
	CostumeIndex   int64  `json:"costume_index"`
	BurstLevel     uint64 `json:"burst_level,omitempty"`
}

func cloneBattlePlan(plan []BattleAction) []BattleAction {
	if plan == nil {
		return nil
	}
	if len(plan) == 0 {
		return make([]BattleAction, 0)
	}
	return append([]BattleAction(nil), plan...)
}

func clipBattlePlan(preset Preset, plan []BattleAction) []BattleAction {
	owned := make(map[uint64]bool, len(preset.Decks))
	for _, deck := range preset.Decks {
		owned[deck.Deck.CharacterInvenIndex] = true
	}
	result := make([]BattleAction, 0, len(plan))
	for _, action := range plan {
		if owned[action.CharacterIndex] {
			result = append(result, action)
		}
	}
	return result
}

func (s *Store) loadBattlePlans(ctx command.Context) error {
	raw, err := s.storage.ListEntries(ctx.State, "deck", "battle_plans")
	if err != nil {
		return err
	}
	for key, payload := range raw {
		slot, err := strconv.ParseUint(key, 10, 64)
		if err != nil || key != strconv.FormatUint(slot, 10) || slot >= s.presetSlots {
			return fmt.Errorf("deck: invalid battle plan key %q", key)
		}
		plan := make([]BattleAction, 0)
		if err := json.Unmarshal(payload, &plan); err != nil {
			return fmt.Errorf("deck: invalid battle plan %q: %w", key, err)
		}
		if plan == nil {
			plan = make([]BattleAction, 0)
		}
		preset, ok := s.presets[slot]
		if !ok {
			return fmt.Errorf("deck: battle plan %q has no preset", key)
		}
		preset.BattlePlan = cloneBattlePlan(plan)
		s.presets[slot] = preset
	}
	return nil
}

func (s *Store) validateBattlePlan(preset Preset, plan []BattleAction) error {
	if len(plan) > len(preset.Decks) {
		return errors.New("battle plan has too many actions")
	}
	characters := make(map[uint64]PresetDeck, len(preset.Decks))
	for _, deck := range preset.Decks {
		characters[deck.Deck.CharacterInvenIndex] = deck
	}
	seen := make(map[uint64]bool, len(plan))
	for _, action := range plan {
		if action.CharacterIndex == 0 || seen[action.CharacterIndex] {
			return errors.New("battle plan has an invalid or repeated character")
		}
		if _, ok := characters[action.CharacterIndex]; !ok {
			return fmt.Errorf("battle plan character %d is not in preset", action.CharacterIndex)
		}
		seen[action.CharacterIndex] = true
		if action.CostumeIndex < -1 {
			return fmt.Errorf("battle plan character %d has invalid action", action.CharacterIndex)
		}
		if action.CostumeIndex > 0 {
			if s.collection == nil {
				return errors.New("battle plan collection unavailable")
			}
			costume, found := s.collection.CostumeByIndex(uint64(action.CostumeIndex))
			if !found || costume.UseChar != action.CharacterIndex {
				return fmt.Errorf("battle plan costume %d does not belong to character %d", action.CostumeIndex, action.CharacterIndex)
			}
			if action.BurstLevel > costume.BurstLevel {
				return fmt.Errorf("battle plan costume %d burst level exceeds owned level", action.CostumeIndex)
			}
		} else if action.BurstLevel != 0 {
			return fmt.Errorf("battle plan character %d has burst without a costume skill", action.CharacterIndex)
		}
	}
	return nil
}

func battlePlanMutation(p Preset) (stateio.EntryMutation, error) {
	payload, err := json.Marshal(p.BattlePlan)
	if err != nil {
		return stateio.EntryMutation{}, err
	}
	return stateio.EntryMutation{Bucket: "battle_plans", Key: strconv.FormatUint(p.Slot, 10), Payload: payload}, nil
}

func battlePlanWire(plan []BattleAction) []byte {
	var out []byte
	for _, action := range plan {
		entry := wire.AppendVarint(nil, 1, action.CharacterIndex)
		entry = wire.AppendVarint(entry, 2, uint64(action.CostumeIndex))
		if action.BurstLevel != 0 {
			entry = wire.AppendVarint(entry, 3, action.BurstLevel)
		}
		out = wire.AppendBytes(out, 2, entry)
	}
	// Presence of schema=1 distinguishes an explicitly empty plan from a
	// missing extension, while preserving the native request's zero defaults.
	return wire.AppendVarint(out, 1, 1)
}

func decodeBattlePlan(data []byte) ([]BattleAction, error) {
	schema, found, err := wire.Varint(data, 1)
	if err != nil || !found || schema != 1 {
		return nil, errors.New("deck: unsupported battle plan schema")
	}
	plan := make([]BattleAction, 0)
	err = wire.Walk(data, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		if field.Type != 2 {
			return errors.New("deck: invalid battle plan action")
		}
		character, ok, e := wire.Varint(field.Value, 1)
		if e != nil || !ok {
			return errors.New("deck: battle action missing character")
		}
		costume, _, e := wire.Varint(field.Value, 2)
		if e != nil {
			return e
		}
		burst, _, e := wire.Varint(field.Value, 3)
		if e != nil {
			return e
		}
		plan = append(plan, BattleAction{CharacterIndex: character, CostumeIndex: int64(costume), BurstLevel: burst})
		return nil
	})
	return plan, err
}
