package monsterhunt

import (
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
	"fmt"
)

func (s *Service) presetBindings(p []byte) (map[uint64]uint64, []player.PresetEquipmentBinding, error) {
	assignments := map[uint64]uint64{}
	var bindings []player.PresetEquipmentBinding
	entries, e := messages(p, 5)
	if e != nil {
		return nil, nil, e
	}
	for _, entry := range entries {
		base, _, e := wire.Bytes(entry, 1)
		if e != nil {
			return nil, nil, e
		}
		index, _ := scalar(base, 1)
		costume, _ := scalar(entry, 2)
		if _, duplicate := assignments[index]; duplicate {
			return nil, nil, fmt.Errorf("monsterhunt: duplicate preset character")
		}
		assignments[index] = costume
		if costume != 0 && s.collection != nil {
			c, found := s.collection.CostumeByIndex(costume)
			if !found || c.UseChar != index {
				return nil, nil, fmt.Errorf("monsterhunt: preset costume not owned by character")
			}
		}
		binding := player.PresetEquipmentBinding{CharacterIndex: index, Equipment: make([]uint64, 5)}
		equipment, e := messages(entry, 3)
		if e != nil {
			return nil, nil, e
		}
		seen := map[uint64]bool{}
		for _, item := range equipment {
			t, _ := scalar(item, 1)
			id, _ := scalar(item, 2)
			if t >= 5 || seen[t] {
				return nil, nil, fmt.Errorf("monsterhunt: invalid preset equipment slot")
			}
			seen[t] = true
			binding.Equipment[t] = id
		}
		bindings = append(bindings, binding)
	}
	if len(bindings) > 0 && s.equipment != nil {
		if e = s.equipment.ValidatePresetEquipment(bindings); e != nil {
			return nil, nil, e
		}
	}
	return assignments, bindings, nil
}
func (s *Service) applyPreset(p []byte) ([]byte, error) {
	if s.characters == nil || s.equipment == nil || s.collection == nil {
		return nil, fmt.Errorf("monsterhunt: preset ownership runtime unavailable")
	}
	assignments, bindings, e := s.presetBindings(p)
	if e != nil {
		return nil, e
	}
	if _, e = s.characters.ApplyPresetCostumes(assignments); e != nil {
		return nil, e
	}
	if len(bindings) > 0 {
		if _, e = s.equipment.ApplyPresetEquipment(bindings); e != nil {
			return nil, e
		}
	}
	var out []byte
	for _, binding := range bindings {
		c, ok := s.characters.Find(binding.CharacterIndex)
		if ok {
			out = wire.AppendBytes(out, 2, player.CharacterWire(c))
		}
		b := wire.AppendVarint(nil, 1, binding.CharacterIndex)
		for _, id := range binding.Equipment {
			b = wire.AppendVarint(b, 2, id)
		}
		out = wire.AppendBytes(out, 3, b)
	}
	return out, nil
}

func (s *Service) validateSettings(settings [][]byte) error {
	for _, setting := range settings {
		index, e := scalar(setting, 1)
		if e != nil || index == 0 {
			return fmt.Errorf("monsterhunt: invalid costume setting character")
		}
		if s.characters != nil {
			if _, ok := s.characters.Find(index); !ok {
				return fmt.Errorf("monsterhunt: setting character not owned")
			}
		}
		seq, e := messages(setting, 2)
		if e != nil {
			return e
		}
		mode, _ := scalar(setting, 3)
		if mode != BattleMode && mode != PracticeMode {
			return fmt.Errorf("monsterhunt: invalid setting battle mode")
		}
		for _, item := range seq {
			costume, _, e := wire.Varint(item, 1)
			if e != nil {
				return e
			}
			if costume == 0 || costume == ^uint64(0) {
				continue
			}
			if s.collection != nil {
				c, ok := s.collection.CostumeByIndex(costume)
				if !ok || c.UseChar != index {
					return fmt.Errorf("monsterhunt: setting costume not owned")
				}
			}
		}
	}
	return nil
}
