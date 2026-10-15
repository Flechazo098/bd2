package ownership

import (
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"fmt"
)

func decodeFields(raw []byte, scalars map[int]*uint64, repeated map[int]*[]uint64, nested func(wire.Field) error) error {
	seen := map[int]bool{}
	return wire.Walk(raw, func(f wire.Field) error {
		if target := scalars[f.Number]; target != nil {
			if f.Type != 0 || seen[f.Number] {
				return fmt.Errorf("ownership: invalid scalar field %d", f.Number)
			}
			seen[f.Number] = true
			*target, _ = binary.Uvarint(f.Value)
			return nil
		}
		if target := repeated[f.Number]; target != nil {
			if f.Type == 0 {
				value, _ := binary.Uvarint(f.Value)
				*target = append(*target, value)
				return nil
			}
			if f.Type != 2 {
				return fmt.Errorf("ownership: invalid repeated field %d", f.Number)
			}
			for rest := f.Value; len(rest) != 0; {
				value, size := binary.Uvarint(rest)
				if size <= 0 {
					return wire.ErrMalformed
				}
				*target = append(*target, value)
				rest = rest[size:]
			}
			return nil
		}
		if nested != nil {
			return nested(f)
		}
		return nil
	})
}

func decodePictorial(f wire.Field, number int, target *[]Pictorial) error {
	if f.Number != number {
		return nil
	}
	if f.Type != 2 {
		return wire.ErrMalformed
	}
	var value Pictorial
	if err := decodeFields(f.Value, map[int]*uint64{1: &value.ID, 2: &value.GroupID}, nil, nil); err != nil {
		return err
	}
	*target = append(*target, value)
	return nil
}

func DecodeCharacter(raw []byte) (Character, error) {
	var c Character
	err := decodeFields(raw, map[int]*uint64{1: &c.InvenIndex, 2: &c.ID, 3: &c.HP, 4: &c.Level, 5: &c.CostumeID, 6: &c.Exp, 7: &c.UseCostume, 8: &c.TalentLevel, 9: &c.TalentExp, 10: &c.SolidarityReward, 11: &c.ExpiryTime, 13: &c.ConnectPotentialCostume}, nil, func(f wire.Field) error { return decodePictorial(f, 12, &c.Pictorialbook) })
	return c, err
}

func DecodeCostume(raw []byte) (Costume, error) {
	var c Costume
	err := decodeFields(raw, map[int]*uint64{1: &c.InvenIndex, 2: &c.ID, 3: &c.Level, 4: &c.UseChar, 6: &c.SortID, 9: &c.DesignID, 10: &c.BurstLevel, 12: &c.TimeValue}, map[int]*[]uint64{8: &c.PotentialIDs, 11: &c.PotentialLockIDs}, func(f wire.Field) error { return decodePictorial(f, 5, &c.Pictorialbook) })
	return c, err
}

func DecodeEquipment(raw []byte) (Equipment, error) {
	var e Equipment
	baseSeen := false
	err := decodeFields(raw, map[int]*uint64{1: &e.InvenIndex, 2: &e.UseChar, 3: &e.KeepFlag, 4: &e.LockFlag, 7: &e.SortID}, nil, func(f wire.Field) error {
		if f.Number == 8 {
			if f.Type != 2 {
				return wire.ErrMalformed
			}
			e.Mark = string(f.Value)
			return nil
		}
		if f.Number != 5 {
			return nil
		}
		if f.Type != 2 || baseSeen {
			return wire.ErrMalformed
		}
		baseSeen = true
		return decodeFields(f.Value, map[int]*uint64{1: &e.ID, 2: &e.Level}, map[int]*[]uint64{6: &e.Rank}, func(option wire.Field) error {
			if option.Number < 3 || option.Number > 5 {
				return nil
			}
			if option.Type != 2 {
				return wire.ErrMalformed
			}
			var value EquipmentOption
			if err := decodeFields(option.Value, map[int]*uint64{1: &value.GroupID, 2: &value.ID}, nil, nil); err != nil {
				return err
			}
			switch option.Number {
			case 3:
				e.MainOption = append(e.MainOption, value)
			case 4:
				e.SubOption = append(e.SubOption, value)
			case 5:
				if e.PrivateOption != nil {
					return wire.ErrMalformed
				}
				e.PrivateOption = &value
			}
			return nil
		})
	})
	return e, err
}
