package inventory

import "bd2server/internal/server/domain/command"

type EquipmentCharacter struct {
	InvenIndex  uint64
	ID          uint64
	TalentLevel uint64
	TalentExp   uint64
	Response    []byte
}
type EquipmentCharacterSource interface {
	EquipmentCharacters() []EquipmentCharacter
	EquipmentCharacter(ctx command.Context, _ uint64) (EquipmentCharacter, bool)
	AddEquipmentTalentExperience(ctx command.Context, _ uint64, _ uint64, _ uint64) (EquipmentCharacter, error)
}
