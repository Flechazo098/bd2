package inventory

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/progression/conditions"
)

// Define_TalentSkillType TS_EQUIPMENT_MAKING, the SubType of McTalentSkillUse.
const talentSkillEquipmentMaking uint64 = 9

// AttachMissionProgress reports equipment actions to mission conditions.
// grade resolves the EquipmentTable grade McEquipMakeGrade compares.
func (s *EquipmentInventory) AttachMissionProgress(record func(command.Context, conditions.Event) error, grade func(equipmentID uint64) uint64) {
	s.missionProgress, s.missionGrade = record, grade
}

func (s *EquipmentInventory) recordMission(ctx command.Context, condition, count uint64) error {
	if s.missionProgress == nil || count == 0 {
		return nil
	}
	return s.missionProgress(ctx, conditions.Event{Type: condition, Count: count})
}

func (s *EquipmentInventory) recordMade(ctx command.Context, created []Equipment) error {
	if s.missionProgress == nil {
		return nil
	}
	for _, entry := range created {
		if err := s.missionProgress(ctx, conditions.Event{Type: conditions.EquipMakeGrade, Sub: s.missionGrade(entry.ID), Count: 1}); err != nil {
			return err
		}
	}
	if len(created) == 0 {
		return nil
	}
	// Equipment making is the TS_EQUIPMENT_MAKING talent skill, used once per
	// request regardless of how many results the recipe produced.
	return s.missionProgress(ctx, conditions.Event{Type: conditions.TalentSkillUse, Sub: talentSkillEquipmentMaking, Count: 1})
}
