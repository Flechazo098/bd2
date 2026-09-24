package player

import (
	"path/filepath"
	"reflect"
	"testing"

	"bd2server/internal/wire"
)

func TestEquipmentPresetSaveInfoRenameAndRestart(t *testing.T) {
	dir := t.TempDir()
	inventory, err := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	character := Character{InvenIndex: 100, ID: 350, Level: 20}
	characters, err := OpenCharacterStore(testStore(filepath.Join(dir, "characters.json")), []Character{character}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	equipment, err := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	if err != nil {
		t.Fatal(err)
	}
	if err := equipment.AttachSlots(map[uint64]uint64{10: 0, 11: 1}); err != nil {
		t.Fatal(err)
	}
	if err := equipment.AttachCharacters(characters); err != nil {
		t.Fatal(err)
	}
	first, _ := equipment.GrantOnce("preset-first", 10)
	second, _ := equipment.GrantOnce("preset-second", 11)
	equipment.mu.Lock()
	next := cloneEquipmentSnapshot(equipment.owned)
	next.Equipment[0].UseChar = character.InvenIndex
	next.Equipment[1].UseChar = character.InvenIndex
	if err := equipment.commitLocked(next, "preset test setup"); err != nil {
		equipment.mu.Unlock()
		t.Fatal(err)
	}
	equipment.mu.Unlock()

	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, character.InvenIndex)
	request = wire.AppendVarint(request, 3, 2)
	request = wire.AppendString(request, 4, "Boss")
	request = wire.AppendVarint(request, 5, 7)
	request = wire.AppendVarint(request, 6, 3)
	for slot, index := range []uint64{first.InvenIndex, second.InvenIndex, 0, 0, 0} {
		item := wire.AppendVarint(nil, 1, uint64(slot))
		if index != 0 {
			item = wire.AppendVarint(item, 2, index)
		}
		request = wire.AppendBytes(request, 7, item)
	}
	if code, _, handled, err := equipment.Handle("/EquipPresetSave", request); err != nil || !handled || code != 254 {
		t.Fatalf("preset save code=%d handled=%v err=%v", code, handled, err)
	}
	assertEquipmentPresetInfo(t, equipment, character.InvenIndex, 2, "Boss", 7, 3, []uint64{first.InvenIndex, second.InvenIndex, 0, 0, 0})

	rename := wire.AppendVarint(nil, 1, 2)
	rename = wire.AppendVarint(rename, 2, character.InvenIndex)
	rename = wire.AppendVarint(rename, 3, 2)
	rename = wire.AppendString(rename, 4, "Raid")
	rename = wire.AppendVarint(rename, 5, 9)
	rename = wire.AppendVarint(rename, 6, 4)
	if code, _, handled, err := equipment.Handle("/EquipPresetNameChange", rename); err != nil || !handled || code != 259 {
		t.Fatalf("preset rename code=%d handled=%v err=%v", code, handled, err)
	}
	assertEquipmentPresetInfo(t, equipment, character.InvenIndex, 2, "Raid", 9, 4, []uint64{first.InvenIndex, second.InvenIndex, 0, 0, 0})

	restarted, err := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.AttachCharacters(characters); err != nil {
		t.Fatal(err)
	}
	assertEquipmentPresetInfo(t, restarted, character.InvenIndex, 2, "Raid", 9, 4, []uint64{first.InvenIndex, second.InvenIndex, 0, 0, 0})
}

func TestEquipmentPresetRejectsForgedEquipment(t *testing.T) {
	dir := t.TempDir()
	inventory, _ := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	characters, _ := OpenCharacterStore(testStore(filepath.Join(dir, "characters.json")), []Character{{InvenIndex: 100, ID: 350, Level: 20}, {InvenIndex: 200, ID: 351, Level: 20}}, inventory, "", "")
	equipment, _ := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	_ = equipment.AttachSlots(map[uint64]uint64{10: 0})
	_ = equipment.AttachCharacters(characters)
	item, _ := equipment.GrantOnce("preset-forged", 10)
	equipment.mu.Lock()
	next := cloneEquipmentSnapshot(equipment.owned)
	next.Equipment[0].UseChar = 200
	_ = equipment.commitLocked(next, "preset forged setup")
	equipment.mu.Unlock()
	request := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 100), 3, 1)
	request = wire.AppendString(request, 4, "Forged")
	request = wire.AppendVarint(request, 5, 1)
	for slot := 0; slot < equipmentSlotCount; slot++ {
		entry := wire.AppendVarint(nil, 1, uint64(slot))
		if slot == 1 {
			entry = wire.AppendVarint(entry, 2, item.InvenIndex)
		}
		request = wire.AppendBytes(request, 7, entry)
	}
	if _, _, handled, err := equipment.Handle("/EquipPresetSave", request); err == nil || !handled {
		t.Fatalf("forged preset handled=%v err=%v", handled, err)
	}
	if len(equipment.presets) != 0 {
		t.Fatal("forged preset was persisted")
	}
}

func TestEquipmentPresetNameChangeCreatesEmptySlot(t *testing.T) {
	dir := t.TempDir()
	inventory, _ := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	characters, _ := OpenCharacterStore(testStore(filepath.Join(dir, "characters.json")), []Character{{InvenIndex: 100, ID: 350, Level: 20}}, inventory, "", "")
	equipment, _ := OpenEquipmentInventory(testStore(filepath.Join(dir, "equipment.json")))
	_ = equipment.AttachCharacters(characters)
	request := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 100), 3, 5)
	request = wire.AppendString(request, 4, "Empty")
	request = wire.AppendVarint(request, 5, 1)
	if code, _, handled, err := equipment.Handle("/EquipPresetNameChange", request); err != nil || !handled || code != 259 {
		t.Fatalf("empty preset rename code=%d handled=%v err=%v", code, handled, err)
	}
	assertEquipmentPresetInfo(t, equipment, 100, 5, "Empty", 1, 0, []uint64{0, 0, 0, 0, 0})
}

func assertEquipmentPresetInfo(t *testing.T, inventory *EquipmentInventory, character, slot uint64, name string, resource, color uint64, wantItems []uint64) {
	t.Helper()
	code, response, handled, err := inventory.Handle("/EquipPresetInfo", wire.AppendVarint(nil, 1, 99))
	if err != nil || !handled || code != 253 {
		t.Fatalf("preset info code=%d handled=%v err=%v", code, handled, err)
	}
	charInfo, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("preset char info found=%v err=%v", found, err)
	}
	if got, _, _ := wire.Varint(charInfo, 1); got != character {
		t.Fatalf("preset character=%d want=%d", got, character)
	}
	preset, found, err := wire.Bytes(charInfo, 2)
	if err != nil || !found {
		t.Fatalf("preset found=%v err=%v", found, err)
	}
	if got, _, _ := wire.Varint(preset, 2); got != slot {
		t.Fatalf("preset slot=%d want=%d", got, slot)
	}
	if got, _, _ := wire.Bytes(preset, 1); string(got) != name {
		t.Fatalf("preset name=%q want=%q", got, name)
	}
	if got, _, _ := wire.Varint(preset, 3); got != resource {
		t.Fatalf("preset resource=%d want=%d", got, resource)
	}
	if got, _, _ := wire.Varint(preset, 4); got != color {
		t.Fatalf("preset color=%d want=%d", got, color)
	}
	var gotItems []uint64
	if err := wire.Walk(preset, func(field wire.Field) error {
		if field.Number == 5 && field.Type == 2 {
			index, _, err := wire.Varint(field.Value, 2)
			if err != nil {
				return err
			}
			gotItems = append(gotItems, index)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotItems, wantItems) {
		t.Fatalf("preset items=%v want=%v", gotItems, wantItems)
	}
}
