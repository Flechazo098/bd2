package roster

import (
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
)

func equipmentCharacter(c Character) assets.EquipmentCharacter {
	return assets.EquipmentCharacter{InvenIndex: c.InvenIndex, ID: c.ID, TalentLevel: c.TalentLevel, TalentExp: c.TalentExp, Response: CharacterWire(c)}
}
func (s *CharacterStore) EquipmentCharacters() []assets.EquipmentCharacter {
	values := s.RawAll()
	result := make([]assets.EquipmentCharacter, 0, len(values))
	for _, c := range values {
		result = append(result, equipmentCharacter(c))
	}
	return result
}
func (s *CharacterStore) EquipmentCharacter(ctx command.Context, index uint64) (assets.EquipmentCharacter, bool) {
	c, found := s.Find(ctx, index)
	return equipmentCharacter(c), found
}

// EquipmentHealth lets an equipment transaction preserve the current HP ratio
// using the same maximum-health calculator as battle and recovery.
func (s *CharacterStore) EquipmentHealth(ctx command.Context, index uint64) (uint64, uint64, error) {
	if s.maxHealth == nil {
		return 0, 0, nil
	}
	maximum, err := s.MaxHealth(ctx, index)
	if err != nil {
		return 0, 0, err
	}
	current, err := s.CurrentHealth(ctx, index)
	return current, maximum, err
}
func (s *CharacterStore) AddEquipmentTalentExperience(ctx command.Context, index, gain, maximum uint64) (assets.EquipmentCharacter, error) {
	c, err := s.AddTalentExperience(ctx, index, gain, maximum)
	return equipmentCharacter(c), err
}
