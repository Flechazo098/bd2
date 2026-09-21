package player

import (
	"path/filepath"
	"testing"

	"bd2server/internal/wire"
)

func TestEquipmentGrantPersistsAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "equipment.json")
	store, err := OpenEquipmentInventory(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.GrantOnce("pack21:quest28:equip10010", 10010)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.GrantOnce("pack21:quest28:equip10010", 10010)
	if err != nil {
		t.Fatal(err)
	}
	if first.InvenIndex != again.InvenIndex || first.ID != again.ID || first.InvenIndex == 0 || first.ID != 10010 {
		t.Fatalf("grants: %+v %+v", first, again)
	}
	restored, err := OpenEquipmentInventory(path)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	code, response, ok, err := restored.Handle("/EquipInfo", request)
	if err != nil || !ok || code != 34 {
		t.Fatalf("handle: %d %v %v", code, ok, err)
	}
	encoded, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("equipment missing: %v", err)
	}
	index, _, _ := wire.Varint(encoded, 1)
	base, _, _ := wire.Bytes(encoded, 5)
	id, _, _ := wire.Varint(base, 1)
	if index != first.InvenIndex || id != 10010 {
		t.Fatalf("wire index=%d id=%d", index, id)
	}
}

func TestEquipmentUsePersistsCharacterBinding(t *testing.T) {
	dir := t.TempDir()
	starter := &Starter{Version: "2.34.13"}
	items, err := OpenInventory(filepath.Join(dir, "items.json"), starter)
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(filepath.Join(dir, "characters.json"),
		[]Character{{InvenIndex: 535607162, ID: 350, Level: 20}}, items, "", "")
	if err != nil {
		t.Fatal(err)
	}
	equipment, err := OpenEquipmentInventory(filepath.Join(dir, "equipment.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := equipment.AttachCharacters(characters); err != nil {
		t.Fatal(err)
	}
	entry, err := equipment.GrantOnce("quest28", 10010)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, entry.InvenIndex)
	request = wire.AppendVarint(request, 3, 535607162)
	code, response, ok, err := equipment.Handle("/EquipUse", request)
	if err != nil || !ok || code != 35 {
		t.Fatalf("use: code=%d ok=%v err=%v", code, ok, err)
	}
	character, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("response character: %v", err)
	}
	index, _, _ := wire.Varint(character, 1)
	if index != 535607162 {
		t.Fatalf("character index=%d", index)
	}
	restored, err := OpenEquipmentInventory(filepath.Join(dir, "equipment.json"))
	if err != nil {
		t.Fatal(err)
	}
	infoRequest := wire.AppendVarint(nil, 1, 2)
	_, info, _, err := restored.Handle("/EquipInfo", infoRequest)
	if err != nil {
		t.Fatal(err)
	}
	encoded, found, err := wire.Bytes(info, 1)
	if err != nil || !found {
		t.Fatalf("restored equipment: %v", err)
	}
	useChar, found, err := wire.Varint(encoded, 2)
	if err != nil || !found || useChar != 535607162 {
		t.Fatalf("useChar=%d found=%v err=%v", useChar, found, err)
	}
}
