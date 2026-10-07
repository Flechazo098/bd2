package inventory

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"errors"
)

func (s *EquipmentInventory) AttachStatDesign(ctx command.Context, design *gamedata.EquipmentStatDesign) error {
	if design == nil {
		return errors.New("player: nil equipment stat design")
	}

	s.statDesign = design
	return nil
}
