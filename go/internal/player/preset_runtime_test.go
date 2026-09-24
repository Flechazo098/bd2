package player

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestValidatePresetEquipmentDoesNotHoldEquipmentLockDuringCharacterStats(t *testing.T) {
	dir := t.TempDir()
	inventory, err := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(testStore(filepath.Join(dir, "characters.json")), []Character{{InvenIndex: 100, ID: 350, Level: 1}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	equipment, err := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	if err != nil {
		t.Fatal(err)
	}
	if err := equipment.AttachSlots(map[uint64]uint64{10: 0}); err != nil {
		t.Fatal(err)
	}
	if err := equipment.AttachCharacters(characters); err != nil {
		t.Fatal(err)
	}
	owned, err := equipment.GrantOnce("preset-deadlock", 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := characters.AttachMaxHealth(func(Character) (uint64, error) {
		// This mirrors the production stat calculator's dependency on the
		// equipment inventory. Validation must not hold equipment.mu here.
		if len(equipment.All()) == 0 {
			return 0, errors.New("missing equipment")
		}
		return 100, nil
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- equipment.ValidatePresetEquipment([]PresetEquipmentBinding{{
			CharacterIndex: 100,
			Equipment:      []uint64{owned.InvenIndex, 0, 0, 0, 0},
		}})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("preset equipment validation deadlocked through character stat calculation")
	}
}
