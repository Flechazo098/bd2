package inventory

import (
	"bd2server/internal/server/domain/command"
	"fmt"
	"math/bits"
	"reflect"
	"slices"
)

// The roster owns current HP; equipment only supplies the before/after stat
// views. A zero maximum means the stat calculator has not been attached yet.
type equipmentHealthSource interface {
	EquipmentHealth(command.Context, uint64) (uint64, uint64, error)
	SetCurrentHealth(command.Context, uint64, uint64) error
}

func sameHealthEquipment(a, b Equipment) bool {
	a.LockFlag, a.KeepFlag, a.SortID, a.UpgradeAttempts, a.Mark = 0, 0, 0, 0, ""
	b.LockFlag, b.KeepFlag, b.SortID, b.UpgradeAttempts, b.Mark = 0, 0, 0, 0, ""
	return reflect.DeepEqual(a, b)
}

func equipmentHealthRatio(current, previousMax, nextMax uint64) uint64 {
	if current == 0 || nextMax == 0 {
		return 0
	}
	if current >= previousMax {
		return nextMax
	}
	hi, lo := bits.Mul64(current, nextMax)
	hp, _ := bits.Div64(hi, lo, previousMax)
	// Changing equipment cannot kill a living character through integer rounding.
	return max(1, hp)
}

func (s *EquipmentInventory) preserveEquipmentHealth(ctx command.Context, next equipmentSnapshot) error {
	health, ok := s.characters.(equipmentHealthSource)
	if !ok {
		return nil
	}
	before := make(map[uint64]Equipment, len(s.persisted.Equipment))
	for _, item := range s.persisted.Equipment {
		before[item.InvenIndex] = item
	}
	affected := map[uint64]bool{}
	for _, item := range next.Equipment {
		old, exists := before[item.InvenIndex]
		if !exists || !sameHealthEquipment(old, item) {
			affected[old.UseChar], affected[item.UseChar] = true, true
		}
		delete(before, item.InvenIndex)
	}
	for _, item := range before {
		affected[item.UseChar] = true
	}
	delete(affected, 0)
	if len(affected) == 0 {
		return nil
	}
	indices := make([]uint64, 0, len(affected))
	for index := range affected {
		indices = append(indices, index)
	}
	slices.Sort(indices)
	original := s.owned
	defer func() { s.owned = original }()
	s.owned = s.persisted
	type snapshot struct{ current, maximum uint64 }
	oldHP := make(map[uint64]snapshot, len(indices))
	for _, index := range indices {
		current, maximum, err := health.EquipmentHealth(ctx, index)
		if err != nil {
			return fmt.Errorf("player: equipment health before change: %w", err)
		}
		if maximum != 0 {
			oldHP[index] = snapshot{current, maximum}
		}
	}
	s.owned = next
	for _, index := range indices {
		old, found := oldHP[index]
		if !found {
			continue
		}
		_, maximum, err := health.EquipmentHealth(ctx, index)
		if err != nil {
			return fmt.Errorf("player: equipment health after change: %w", err)
		}
		if maximum == old.maximum && old.current <= maximum {
			continue
		}
		if maximum == 0 {
			return fmt.Errorf("player: equipment left character %d without maximum health", index)
		}
		if err := health.SetCurrentHealth(ctx, index, equipmentHealthRatio(old.current, old.maximum, maximum)); err != nil {
			return err
		}
	}
	return nil
}
