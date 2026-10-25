package inventory_test

import (
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	accountstate "bd2server/internal/server/storage/account"
	"bd2server/internal/server/storage/instanceid"
	"bd2server/internal/server/storage/stateio"
	"context"
	"path/filepath"
	"testing"
)

func TestPresetEquipmentPreservesHealthRatiosAndPersistsTransfers(t *testing.T) {
	root := t.TempDir()
	repo, err := accountstate.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	ids := instanceid.New(filepath.Join(root, "instance-ids.db"))
	var first, second uint64
	run := func(commit bool, action func(command.Context, *assets.EquipmentInventory, *roster.CharacterStore)) {
		t.Helper()
		tx, err := repo.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		ctx := command.Context{State: tx, InstanceIDs: ids}
		scope := stateio.RootStore{}
		items, err := assets.OpenInventory(ctx, scope, nil)
		if err != nil {
			t.Fatal(err)
		}
		characters, err := roster.OpenCharacterStore(ctx, scope, []roster.Character{
			{InvenIndex: 11, ID: 1, Level: 1, HP: 1000},
			{InvenIndex: 12, ID: 2, Level: 1, HP: 1000},
			{InvenIndex: 13, ID: 3, Level: 1, HP: 0},
		}, items, "", "")
		if err != nil {
			t.Fatal(err)
		}
		equipment, err := assets.OpenEquipmentInventory(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		if err := equipment.AttachCharacters(ctx, characters); err != nil {
			t.Fatal(err)
		}
		if err := equipment.AttachSlots(ctx, map[uint64]uint64{70: 0, 71: 0}); err != nil {
			t.Fatal(err)
		}
		if err := characters.AttachMaxHealth(ctx, func(_ command.Context, c roster.Character) (uint64, error) {
			maximum := uint64(1000)
			for _, item := range equipment.All(ctx) {
				if item.UseChar == c.InvenIndex {
					maximum += map[uint64]uint64{70: 1000, 71: 3000}[item.ID]
				}
			}
			return maximum, nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := characters.EnsurePersisted(ctx); err != nil {
			t.Fatal(err)
		}
		action(ctx, equipment, characters)
		if commit {
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
	}
	bind := func(character, weapon uint64) assets.PresetEquipmentBinding {
		return assets.PresetEquipmentBinding{CharacterIndex: character, Equipment: []uint64{weapon, 0, 0, 0, 0}}
	}
	check := func(ctx command.Context, chars *roster.CharacterStore, index, want uint64) {
		t.Helper()
		got, err := chars.CurrentHealth(ctx, index)
		if err != nil || got != want {
			t.Fatalf("character %d current HP=%d, error=%v, want %d", index, got, err, want)
		}
	}
	run(true, func(ctx command.Context, equipment *assets.EquipmentInventory, chars *roster.CharacterStore) {
		a, err := equipment.GrantOnce(ctx, "a", 70)
		if err != nil {
			t.Fatal(err)
		}
		b, err := equipment.GrantOnce(ctx, "b", 71)
		if err != nil {
			t.Fatal(err)
		}
		first, second = a.InvenIndex, b.InvenIndex
		if err := chars.SetCurrentHealth(ctx, 12, 500); err != nil {
			t.Fatal(err)
		}
		if _, err := equipment.ApplyPresetEquipment(ctx, []assets.PresetEquipmentBinding{bind(11, first)}); err != nil {
			t.Fatal(err)
		}
		check(ctx, chars, 11, 2000)
	})
	run(true, func(ctx command.Context, equipment *assets.EquipmentInventory, chars *roster.CharacterStore) {
		if _, err := equipment.ApplyPresetEquipment(ctx, []assets.PresetEquipmentBinding{bind(11, second), bind(12, first)}); err != nil {
			t.Fatal(err)
		}
		check(ctx, chars, 11, 4000) // Full HP remains full when the maximum increases.
		check(ctx, chars, 12, 1000) // Half HP remains half; it is not healed to full.
	})
	run(false, func(ctx command.Context, equipment *assets.EquipmentInventory, chars *roster.CharacterStore) {
		if _, err := equipment.ApplyPresetEquipment(ctx, []assets.PresetEquipmentBinding{bind(12, second)}); err != nil {
			t.Fatal(err)
		}
		check(ctx, chars, 11, 1000) // The donor loses its gear and remains full.
		check(ctx, chars, 12, 2000)
	})
	run(true, func(ctx command.Context, equipment *assets.EquipmentInventory, chars *roster.CharacterStore) {
		check(ctx, chars, 11, 4000) // The previous transaction rolled back HP and gear.
		check(ctx, chars, 12, 1000)
		if _, err := equipment.ApplyPresetEquipment(ctx, []assets.PresetEquipmentBinding{bind(12, second), bind(13, first)}); err != nil {
			t.Fatal(err)
		}
		check(ctx, chars, 11, 1000)
		check(ctx, chars, 12, 2000)
		check(ctx, chars, 13, 0) // Equipping an exhausted character never revives it.
	})
	run(true, func(ctx command.Context, equipment *assets.EquipmentInventory, chars *roster.CharacterStore) {
		check(ctx, chars, 11, 1000)
		check(ctx, chars, 12, 2000)
		check(ctx, chars, 13, 0)
	})
}
