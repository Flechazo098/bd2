package player

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"bd2server/internal/wire"
)

func TestEquipmentBatchUseOneClickClearPreservesUnfilteredSlots(t *testing.T) {
	fixture := newEquipmentBatchUseFixture(t)
	weapon := fixture.grant(t, "clear-weapon", 10, 100)
	armor := fixture.grant(t, "clear-armor", 11, 100)

	code, response, handled, err := fixture.equipment.Handle("/EquipBatchUse", equipmentBatchUseRequest(
		equipmentBatchUseEntry(100, 0, armor.InvenIndex, 0, 0, 0),
	))
	if err != nil || !handled || code != 276 {
		t.Fatalf("batch clear code=%d handled=%v err=%v", code, handled, err)
	}
	if got := responseCharacterIndices(t, response); !reflect.DeepEqual(got, []uint64{100}) {
		t.Fatalf("response characters=%v", got)
	}
	assertEquipmentOwners(t, fixture.equipment, map[uint64]uint64{
		weapon.InvenIndex: 0,
		armor.InvenIndex:  100,
	})
}

func TestEquipmentBatchUseAutoMountReplacesOnlyRequestedSlot(t *testing.T) {
	fixture := newEquipmentBatchUseFixture(t)
	oldWeapon := fixture.grant(t, "auto-old-weapon", 10, 100)
	armor := fixture.grant(t, "auto-armor", 11, 100)
	newWeapon := fixture.grant(t, "auto-new-weapon", 12, 0)

	code, response, handled, err := fixture.equipment.Handle("/EquipBatchUse", equipmentBatchUseRequest(
		equipmentBatchUseEntry(100, newWeapon.InvenIndex, armor.InvenIndex, 0, 0, 0),
	))
	if err != nil || !handled || code != 276 {
		t.Fatalf("auto mount code=%d handled=%v err=%v", code, handled, err)
	}
	if got := responseCharacterIndices(t, response); !reflect.DeepEqual(got, []uint64{100}) {
		t.Fatalf("response characters=%v", got)
	}
	assertEquipmentOwners(t, fixture.equipment, map[uint64]uint64{
		oldWeapon.InvenIndex: 0,
		armor.InvenIndex:     100,
		newWeapon.InvenIndex: 100,
	})
}

func TestEquipmentBatchUseTransfersAcrossCharactersReturnsBothAndPersists(t *testing.T) {
	fixture := newEquipmentBatchUseFixture(t)
	transferred := fixture.grant(t, "cross-character", 10, 200)

	code, response, handled, err := fixture.equipment.Handle("/EquipBatchUse", equipmentBatchUseRequest(
		equipmentBatchUseEntry(200, 0, 0, 0, 0, 0),
		equipmentBatchUseEntry(100, transferred.InvenIndex, 0, 0, 0, 0),
	))
	if err != nil || !handled || code != 276 {
		t.Fatalf("cross-character batch code=%d handled=%v err=%v", code, handled, err)
	}
	// The target changes and the previous owner loses equipment. Both
	// characters must be returned so the client can refresh their derived HP.
	if got := responseCharacterIndices(t, response); !reflect.DeepEqual(got, []uint64{100, 200}) {
		t.Fatalf("response characters=%v", got)
	}
	assertEquipmentOwners(t, fixture.equipment, map[uint64]uint64{transferred.InvenIndex: 100})

	restarted, err := OpenEquipmentInventory(testStore(fixture.equipmentPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.AttachSlots(fixture.slots); err != nil {
		t.Fatal(err)
	}
	if err := restarted.AttachCharacters(fixture.characters); err != nil {
		t.Fatal(err)
	}
	assertEquipmentOwners(t, restarted, map[uint64]uint64{transferred.InvenIndex: 100})
}

func TestEquipmentBatchUseRejectsInvalidSnapshotsAtomically(t *testing.T) {
	fixture := newEquipmentBatchUseFixture(t)
	weapon := fixture.grant(t, "invalid-weapon", 10, 100)
	armor := fixture.grant(t, "invalid-armor", 11, 100)
	want := fixture.equipment.All()

	tests := []struct {
		name    string
		request []byte
	}{
		{
			name: "duplicate equipment",
			request: equipmentBatchUseRequest(
				equipmentBatchUseEntry(100, weapon.InvenIndex, weapon.InvenIndex, 0, 0, 0),
			),
		},
		{
			name: "equipment in wrong slot",
			request: equipmentBatchUseRequest(
				equipmentBatchUseEntry(100, 0, weapon.InvenIndex, 0, 0, 0),
			),
		},
		{
			name: "unknown equipment",
			request: equipmentBatchUseRequest(
				equipmentBatchUseEntry(100, 999999999, armor.InvenIndex, 0, 0, 0),
			),
		},
		{
			name: "unknown character",
			request: equipmentBatchUseRequest(
				equipmentBatchUseEntry(999, weapon.InvenIndex, armor.InvenIndex, 0, 0, 0),
			),
		},
		{
			name: "transfer without previous owner snapshot",
			request: equipmentBatchUseRequest(
				equipmentBatchUseEntry(200, weapon.InvenIndex, 0, 0, 0, 0),
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, handled, err := fixture.equipment.Handle("/EquipBatchUse", test.request); err == nil || !handled {
				t.Fatalf("invalid batch handled=%v err=%v", handled, err)
			}
			if got := fixture.equipment.All(); !reflect.DeepEqual(got, want) {
				t.Fatalf("invalid batch mutated state:\n got=%+v\nwant=%+v", got, want)
			}
		})
	}

	// Reopening the store proves rejected requests also left no partial disk
	// mutation behind.
	restarted, err := OpenEquipmentInventory(testStore(fixture.equipmentPath))
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.All(); !reflect.DeepEqual(got, want) {
		t.Fatalf("invalid batches mutated persisted state:\n got=%+v\nwant=%+v", got, want)
	}
}

type equipmentBatchUseFixture struct {
	equipmentPath string
	equipment     *EquipmentInventory
	characters    *CharacterStore
	slots         map[uint64]uint64
}

func newEquipmentBatchUseFixture(t *testing.T) *equipmentBatchUseFixture {
	t.Helper()
	dir := t.TempDir()
	inventory, err := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(testStore(filepath.Join(dir, "characters.json")), []Character{
		{InvenIndex: 100, ID: 350, Level: 20},
		{InvenIndex: 200, ID: 351, Level: 20},
	}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "equipment.json")
	equipment, err := OpenEquipmentInventory(testStore(path))
	if err != nil {
		t.Fatal(err)
	}
	slots := map[uint64]uint64{10: 0, 11: 1, 12: 0, 13: 2, 14: 3, 15: 4}
	if err := equipment.AttachSlots(slots); err != nil {
		t.Fatal(err)
	}
	if err := equipment.AttachCharacters(characters); err != nil {
		t.Fatal(err)
	}
	return &equipmentBatchUseFixture{
		equipmentPath: path,
		equipment:     equipment,
		characters:    characters,
		slots:         slots,
	}
}

func (f *equipmentBatchUseFixture) grant(t *testing.T, identity string, designID, character uint64) Equipment {
	t.Helper()
	entry, err := f.equipment.GrantOnce(identity, designID)
	if err != nil {
		t.Fatal(err)
	}
	if character == 0 {
		return entry
	}
	f.equipment.mu.Lock()
	next := cloneEquipmentSnapshot(f.equipment.owned)
	for i := range next.Equipment {
		if next.Equipment[i].InvenIndex == entry.InvenIndex {
			next.Equipment[i].UseChar = character
			entry = next.Equipment[i]
			break
		}
	}
	if err := f.equipment.commitLocked(next, "batch-use test setup"); err != nil {
		f.equipment.mu.Unlock()
		t.Fatal(err)
	}
	f.equipment.mu.Unlock()
	return entry
}

func equipmentBatchUseRequest(entries ...[]byte) []byte {
	request := wire.AppendVarint(nil, 1, 1)
	for _, entry := range entries {
		request = wire.AppendBytes(request, 2, entry)
	}
	return request
}

func equipmentBatchUseEntry(character uint64, equipment ...uint64) []byte {
	entry := wire.AppendVarint(nil, 1, character)
	for _, index := range equipment {
		entry = wire.AppendVarint(entry, 2, index)
	}
	return entry
}

func responseCharacterIndices(t *testing.T, response []byte) []uint64 {
	t.Helper()
	var result []uint64
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number != 1 {
			return nil
		}
		index, found, err := wire.Varint(field.Value, 1)
		if err != nil {
			return err
		}
		if !found || index == 0 {
			t.Fatalf("response contains invalid CharDBInfo")
		}
		result = append(result, index)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func assertEquipmentOwners(t *testing.T, inventory *EquipmentInventory, want map[uint64]uint64) {
	t.Helper()
	got := make(map[uint64]uint64)
	for _, equipment := range inventory.All() {
		got[equipment.InvenIndex] = equipment.UseChar
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("equipment owners=%v want=%v", got, want)
	}
}
