package player

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"bd2server/internal/gamedata"
	"bd2server/internal/wire"
)

func TestCharImmortalReturnsFullOwnedSnapshot(t *testing.T) {
	dir := t.TempDir()
	starter := &Starter{Version: "2.34.13"}
	inventory, err := OpenInventory(filepath.Join(dir, "items.json"), starter)
	if err != nil {
		t.Fatal(err)
	}
	owned := []Character{
		{InvenIndex: 535604120, ID: 6010, HP: 7, Level: 1, TalentLevel: 1},
		{InvenIndex: 535607162, ID: 350, HP: 512, Level: 20, TalentLevel: 1},
	}
	characters, err := OpenCharacterStore(filepath.Join(dir, "characters.json"), owned, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	packed := binary.AppendUvarint(nil, owned[0].InvenIndex)
	request := wire.AppendBytes(wire.AppendVarint(nil, 1, 23), 2, packed)
	code, response, ok, err := characters.Handle("/CharImmortal", request)
	if err != nil || !ok || code != 96 {
		t.Fatalf("immortal: code=%d handled=%v err=%v", code, ok, err)
	}
	encoded, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("missing revived character: %v", err)
	}
	index, _, _ := wire.Varint(encoded, 1)
	hp, _, _ := wire.Varint(encoded, 3)
	if index != owned[0].InvenIndex || hp != 7 {
		t.Fatalf("revived character index=%d hp=%d", index, hp)
	}
	for _, invalid := range [][]byte{
		wire.AppendVarint(wire.AppendVarint(nil, 1, 24), 2, 999),
		wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 25), 2, owned[0].InvenIndex), 2, owned[0].InvenIndex),
		wire.AppendVarint(nil, 1, 26),
	} {
		if _, _, handled, err := characters.Handle("/CharImmortal", invalid); err == nil || !handled {
			t.Fatalf("invalid immortal request accepted: handled=%v err=%v", handled, err)
		}
	}
}

func TestCharacterGrowthConsumesMaterialAndPersists(t *testing.T) {
	dir := t.TempDir()
	starter := &Starter{Version: "2.34.13"}
	inventory, err := OpenInventory(filepath.Join(dir, "items.json"), starter)
	if err != nil {
		t.Fatal(err)
	}
	items, err := inventory.GrantOnce("battle", []gamedata.BattleReward{{Type: 8, ID: 8, Count: 3}})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(filepath.Join(dir, "characters.json"), []Character{{InvenIndex: 77, ID: 350, Level: 1}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	characters.grow = func(Character, []gamedata.GrowthMaterial) (uint64, uint64, []gamedata.GrowthMaterial, error) {
		return 20, 0, []gamedata.GrowthMaterial{{ID: 7, Count: 6}}, nil
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, 77)
	material := ItemWire(items[0])
	request = wire.AppendBytes(request, 3, material)
	code, response, ok, err := characters.Handle("/CharGrowth", request)
	if err != nil || !ok || code != 433 {
		t.Fatalf("growth: code=%d ok=%v err=%v", code, ok, err)
	}
	encoded, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("character response: %v", err)
	}
	level, found, err := wire.Varint(encoded, 4)
	if err != nil || !found || level != 20 {
		t.Fatalf("level=%d found=%v err=%v", level, found, err)
	}
	restored, err := OpenCharacterStore(filepath.Join(dir, "characters.json"), nil, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.All(); len(got) != 1 || got[0].Level != 20 {
		t.Fatalf("restored=%+v", got)
	}
	_, itemResponse, _, err := inventory.Handle("/ItemInfo", wire.AppendVarint(nil, 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	owned, found, err := wire.Bytes(itemResponse, 1)
	if err != nil || !found {
		t.Fatalf("refund absent: found=%v err=%v", found, err)
	}
	id, _, _ := wire.Varint(owned, 2)
	count, _, _ := wire.Varint(owned, 4)
	if id != 7 || count != 6 {
		t.Fatalf("wrong refund id=%d count=%d", id, count)
	}
	bundle, found, err := wire.Bytes(response, 2)
	if err != nil || !found {
		t.Fatalf("missing growth reward bundle: %v", err)
	}
	reward, found, err := wire.Bytes(bundle, 1)
	if err != nil || !found {
		t.Fatalf("missing reward item: %v", err)
	}
	rewardID, _, _ := wire.Varint(reward, 2)
	if rewardID != 7 {
		t.Fatalf("reward id=%d", rewardID)
	}
	if err := inventory.Consume(items); err == nil {
		t.Fatal("already spent growth material accepted again")
	}
}

func TestGrowthAndImmortalShareDynamicMaximumHealth(t *testing.T) {
	dir := t.TempDir()
	inventory, err := OpenInventory(filepath.Join(dir, "items.json"), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := inventory.GrantOnce("growth", []gamedata.BattleReward{{Type: 8, ID: 8, Count: 2}})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(filepath.Join(dir, "characters.json"), []Character{{InvenIndex: 77, ID: 350, HP: 122, Level: 1}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	characters.grow = func(Character, []gamedata.GrowthMaterial) (uint64, uint64, []gamedata.GrowthMaterial, error) {
		return 20, 0, nil, nil
	}
	if err := characters.AttachMaxHealth(func(character Character) (uint64, error) {
		// The callback must be safe to query account ownership while growth
		// changes the same CharacterStore.
		if len(characters.RawAll()) != 1 {
			t.Fatal("owned character disappeared during health calculation")
		}
		if character.Level == 20 {
			return 513, nil
		}
		return 122, nil
	}); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 11), 2, 77)
	request = wire.AppendBytes(request, 3, ItemWire(items[0]))
	_, response, _, err := characters.Handle("/CharGrowth", request)
	if err != nil {
		t.Fatal(err)
	}
	grown, _, err := wire.Bytes(response, 1)
	if err != nil {
		t.Fatal(err)
	}
	if hp, _, _ := wire.Varint(grown, 3); hp != 513 {
		t.Fatalf("grown HP=%d want 513", hp)
	}
	immortal := wire.AppendVarint(wire.AppendVarint(nil, 1, 12), 2, 77)
	_, response, _, err = characters.Handle("/CharImmortal", immortal)
	if err != nil {
		t.Fatal(err)
	}
	revived, _, err := wire.Bytes(response, 1)
	if err != nil {
		t.Fatal(err)
	}
	if hp, _, _ := wire.Varint(revived, 3); hp != 513 {
		t.Fatalf("revived HP=%d want 513", hp)
	}
	data, err := os.ReadFile(filepath.Join(dir, "characters.json"))
	if err != nil || !bytes.Contains(data, []byte(`"hp":513`)) {
		t.Fatalf("growth HP was not persisted: %s err=%v", data, err)
	}
}

func TestCharacterStoreMergesNewSeedCharacters(t *testing.T) {
	dir := t.TempDir()
	starter := &Starter{Version: "2.34.13"}
	inventory, err := OpenInventory(filepath.Join(dir, "items.json"), starter)
	if err != nil {
		t.Fatal(err)
	}
	first, err := OpenCharacterStore(filepath.Join(dir, "characters.json"), []Character{{InvenIndex: 1, ID: 10, Level: 1}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.persist(first.All()); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenCharacterStore(filepath.Join(dir, "characters.json"), []Character{{InvenIndex: 1, ID: 10, Level: 1}, {InvenIndex: 2, ID: 20, Level: 15}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.All(); len(got) != 2 || got[1].InvenIndex != 2 {
		t.Fatalf("merged=%+v", got)
	}
}
