package deck

import (
	"reflect"
	"sort"
	"testing"

	"bd2server/internal/player"
	"bd2server/internal/stateio"
	"bd2server/internal/wire"
)

type presetFixture struct {
	storage    *stateio.Memory
	seed       Seed
	deck       *Store
	wallet     *player.Wallet
	characters *player.CharacterStore
	equipment  *player.EquipmentInventory
	collection *player.CollectionStore
	weapon     player.Equipment
}

func newPresetFixture(t *testing.T) *presetFixture {
	t.Helper()
	storage := stateio.NewMemory()
	seed, err := LoadSeed("../../seed/v2_34_13/decks.json")
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := player.OpenInventory(storage, &player.Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := player.OpenCollectionStore(storage, []player.Costume{
		{InvenIndex: 1001, ID: 3501, UseChar: 100, BurstLevel: 3},
		{InvenIndex: 2001, ID: 3601, UseChar: 200, BurstLevel: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := player.OpenCharacterStore(storage, []player.Character{
		{InvenIndex: 100, ID: 350, Level: 20, UseCostume: 1001, CostumeID: 3501},
		{InvenIndex: 200, ID: 360, Level: 20, UseCostume: 2001, CostumeID: 3601},
	}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := characters.AttachCollection(collection); err != nil {
		t.Fatal(err)
	}
	equipment, err := player.OpenEquipmentInventory(storage)
	if err != nil {
		t.Fatal(err)
	}
	if err := equipment.AttachSlots(map[uint64]uint64{10: 0, 11: 1, 12: 2, 13: 3, 14: 4}); err != nil {
		t.Fatal(err)
	}
	if err := equipment.AttachCharacters(characters); err != nil {
		t.Fatal(err)
	}
	weapon, err := equipment.GrantOnce("preset-test-weapon", 10)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{Gold: 20000})
	if err != nil {
		t.Fatal(err)
	}
	decks, err := OpenStore(storage, seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := decks.AttachPresetRuntime(wallet, characters, equipment, collection); err != nil {
		t.Fatal(err)
	}
	decks.BeginSession("preset-test")
	return &presetFixture{storage: storage, seed: seed, deck: decks, wallet: wallet, characters: characters, equipment: equipment, collection: collection, weapon: weapon}
}

func presetRequest(slot, character, costume, equipment uint64, name string) []byte {
	base := wire.AppendVarint(nil, 1, character)
	base = wire.AppendVarint(base, 2, 0)
	base = wire.AppendVarint(base, 3, 1)
	deck := wire.AppendBytes(nil, 1, base)
	deck = wire.AppendVarint(deck, 2, costume)
	for equipmentType := uint64(0); equipmentType < 5; equipmentType++ {
		entry := wire.AppendVarint(nil, 1, equipmentType)
		if equipmentType == 0 {
			entry = wire.AppendVarint(entry, 2, equipment)
		}
		deck = wire.AppendBytes(deck, 3, entry)
	}
	preset := wire.AppendString(nil, 1, name)
	preset = wire.AppendVarint(preset, 2, 1)
	preset = wire.AppendVarint(preset, 4, slot)
	preset = wire.AppendBytes(preset, 5, deck)
	return wire.AppendBytes(nil, 2, preset)
}

func TestPresetSaveInfoMetadataDeleteAndRestart(t *testing.T) {
	f := newPresetFixture(t)
	for seq, slot := range []uint64{2, 0} {
		request := req(uint64(seq+1), presetRequest(slot, 100, 1001, f.weapon.InvenIndex, "编队"))
		if code, _, handled, err := f.deck.Handle("/PresetSave", request); err != nil || !handled || code != 179 {
			t.Fatalf("save slot %d code=%d handled=%v err=%v", slot, code, handled, err)
		}
	}
	code, response, handled, err := f.deck.Handle("/PresetInfo", req(10))
	if err != nil || !handled || code != 178 {
		t.Fatalf("info code=%d handled=%v err=%v", code, handled, err)
	}
	var slots []uint64
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number == 1 {
			slot, _, err := wire.Varint(field.Value, 4)
			if err != nil {
				return err
			}
			slots = append(slots, slot)
		}
		return nil
	}); err != nil || !reflect.DeepEqual(slots, []uint64{0, 2}) {
		t.Fatalf("ordered slots=%v err=%v", slots, err)
	}

	change := wire.AppendString(nil, 2, "主力队")
	change = wire.AppendVarint(change, 3, 21)
	change = wire.AppendVarint(change, 4, 5)
	// slot=0 is absent on the real proto3 wire.
	if code, _, _, err := f.deck.Handle("/PresetInfoChange", req(11, change)); err != nil || code != 278 {
		t.Fatalf("metadata change code=%d err=%v", code, err)
	}
	if got := f.deck.presets[0]; got.Name != "主力队" || got.ResourceID != 21 || got.ResourceColor != 5 || len(got.Decks) != 1 {
		t.Fatalf("metadata change lost content: %+v", got)
	}

	restarted, err := OpenStore(f.storage, f.seed)
	if err != nil {
		t.Fatal(err)
	}
	if len(restarted.presets) != 2 || restarted.presets[0].Name != "主力队" {
		t.Fatalf("restarted presets=%+v", restarted.presets)
	}
	restarted.BeginSession("restart")
	deleteRequest := req(12, wire.AppendVarint(nil, 2, 0), wire.AppendVarint(nil, 2, 2))
	if code, _, handled, err := restarted.Handle("/PresetDelete", deleteRequest); err != nil || !handled || code != 0 {
		t.Fatalf("delete code=%d handled=%v err=%v", code, handled, err)
	}
	if len(restarted.presets) != 0 || restarted.presetSlots != 5 {
		t.Fatalf("delete changed slots/content: slots=%d presets=%+v", restarted.presetSlots, restarted.presets)
	}
}

func TestPresetMetadataCanCreateEmptySlot(t *testing.T) {
	f := newPresetFixture(t)
	change := wire.AppendString(nil, 2, "备用")
	change = wire.AppendVarint(change, 3, 2)
	change = wire.AppendVarint(change, 4, 1)
	change = wire.AppendVarint(change, 5, 4)
	if code, _, _, err := f.deck.Handle("/PresetInfoChange", req(1, change)); err != nil || code != 278 {
		t.Fatalf("upsert code=%d err=%v", code, err)
	}
	if got := f.deck.presets[4]; got.Name != "备用" || len(got.Decks) != 0 {
		t.Fatalf("empty metadata preset=%+v", got)
	}
}

func TestPresetAddSlotChargesOncePersistsAndCaps(t *testing.T) {
	f := newPresetFixture(t)
	add := req(20, wire.AppendVarint(nil, 2, 1))
	for attempt := 0; attempt < 2; attempt++ {
		if code, _, _, err := f.deck.Handle("/PresetAddSlot", add); err != nil || code != 180 {
			t.Fatalf("add attempt %d code=%d err=%v", attempt, code, err)
		}
	}
	if f.deck.PresetSlotCount() != 6 || f.wallet.Snapshot().Gold != 18000 {
		t.Fatalf("slots=%d gold=%d", f.deck.PresetSlotCount(), f.wallet.Snapshot().Gold)
	}
	restarted, err := OpenStore(f.storage, f.seed)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.PresetSlotCount() != 6 {
		t.Fatalf("restarted slots=%d", restarted.PresetSlotCount())
	}
	if err := restarted.AttachPresetRuntime(f.wallet, f.characters, f.equipment, f.collection); err != nil {
		t.Fatal(err)
	}
	restarted.BeginSession("cap")
	if _, _, _, err := restarted.Handle("/PresetAddSlot", req(21, wire.AppendVarint(nil, 2, 6))); err != nil {
		t.Fatalf("buy remaining slots: %v", err)
	}
	if _, _, _, err := restarted.Handle("/PresetAddSlot", req(22, wire.AppendVarint(nil, 2, 1))); err == nil {
		t.Fatal("accepted slot beyond maximum")
	}
}

func TestPresetUseAppliesDeckCostumeEquipmentWithoutChangingFieldDeck(t *testing.T) {
	f := newPresetFixture(t)
	wantField := append([]FieldEntry(nil), f.deck.state.FieldDeck...)
	if _, _, _, err := f.deck.Handle("/PresetSave", req(1, presetRequest(0, 100, 1001, f.weapon.InvenIndex, "应用"))); err != nil {
		t.Fatal(err)
	}
	code, response, handled, err := f.deck.Handle("/PresetUse", req(2, wire.AppendVarint(nil, 3, 999999999)))
	if err != nil || !handled || code != 409 {
		t.Fatalf("use code=%d handled=%v err=%v", code, handled, err)
	}
	if len(f.deck.state.Deck) != 1 || f.deck.state.Deck[0].CharacterInvenIndex != 100 || !reflect.DeepEqual(f.deck.state.FieldDeck, wantField) {
		t.Fatalf("deck=%+v field changed=%v", f.deck.state.Deck, !reflect.DeepEqual(f.deck.state.FieldDeck, wantField))
	}
	owned := f.equipment.All()
	if len(owned) != 1 || owned[0].UseChar != 100 {
		t.Fatalf("equipment=%+v", owned)
	}
	var deckCount, characterCount int
	var equipmentValues []uint64
	if err := wire.Walk(response, func(field wire.Field) error {
		switch field.Number {
		case 1:
			deckCount++
		case 2:
			characterCount++
		case 3:
			return wire.Walk(field.Value, func(nested wire.Field) error {
				if nested.Number == 2 {
					values, err := repeatedUint64(nested)
					equipmentValues = append(equipmentValues, values...)
					return err
				}
				return nil
			})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if deckCount != 1 || characterCount == 0 || !reflect.DeepEqual(equipmentValues, []uint64{f.weapon.InvenIndex, 0, 0, 0, 0}) {
		t.Fatalf("response deck=%d chars=%d equipment=%v", deckCount, characterCount, equipmentValues)
	}
	// The client-supplied power is deliberately ignored; replay returns the
	// exact same response and does not repeat cross-domain state transitions.
	if replayCode, replay, _, replayErr := f.deck.Handle("/PresetUse", req(2)); replayErr != nil || replayCode != 409 || !reflect.DeepEqual(replay, response) {
		t.Fatalf("replay code=%d equal=%v err=%v", replayCode, reflect.DeepEqual(replay, response), replayErr)
	}
}

func TestPresetSaveRejectsForgedOwnershipAndEquipmentSlot(t *testing.T) {
	f := newPresetFixture(t)
	tests := []struct {
		name      string
		character uint64
		costume   uint64
		equipment uint64
	}{
		{name: "character", character: 999, costume: 1001, equipment: f.weapon.InvenIndex},
		{name: "costume owner", character: 100, costume: 2001, equipment: f.weapon.InvenIndex},
		{name: "equipment", character: 100, costume: 1001, equipment: 999999},
	}
	for seq, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, err := f.deck.Handle("/PresetSave", req(uint64(seq+1), presetRequest(0, test.character, test.costume, test.equipment, "伪造"))); err == nil {
				t.Fatal("forged preset accepted")
			}
		})
	}
	if len(f.deck.presets) != 0 {
		t.Fatalf("rejected save mutated presets=%+v", f.deck.presets)
	}
}

func TestDeckCostumeSettingSentinelsClearAndRestart(t *testing.T) {
	f := newPresetFixture(t)
	setting := wire.AppendVarint(nil, 1, 100)
	for _, item := range []struct {
		index int64
		burst uint64
	}{{1001, 3}, {-1, 0}, {0, 0}} {
		entry := wire.AppendVarint(nil, 1, uint64(item.index))
		entry = wire.AppendVarint(entry, 2, item.burst)
		setting = wire.AppendBytes(setting, 2, entry)
	}
	if code, _, _, err := f.deck.Handle("/DeckCostumeSettingSave", req(1, wire.AppendBytes(nil, 2, setting))); err != nil || code != 398 {
		t.Fatalf("save setting code=%d err=%v", code, err)
	}
	restarted, err := OpenStore(f.storage, f.seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.AttachPresetRuntime(f.wallet, f.characters, f.equipment, f.collection); err != nil {
		t.Fatal(err)
	}
	restarted.BeginSession("costume-restart")
	code, response, _, err := restarted.Handle("/DeckCostumeSettingInfo", req(2))
	if err != nil || code != 397 {
		t.Fatalf("info setting code=%d err=%v", code, err)
	}
	var got []int64
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number != 1 {
			return nil
		}
		return wire.Walk(field.Value, func(nested wire.Field) error {
			if nested.Number == 2 {
				value, _, err := wire.Varint(nested.Value, 1)
				got = append(got, int64(value))
				return err
			}
			return nil
		})
	}); err != nil || !reflect.DeepEqual(got, []int64{1001, -1, 0}) {
		t.Fatalf("setting sequence=%v err=%v", got, err)
	}

	clear := wire.AppendVarint(nil, 1, 100)
	if _, _, _, err := restarted.Handle("/DeckCostumeSettingSave", req(3, wire.AppendBytes(nil, 2, clear))); err != nil {
		t.Fatal(err)
	}
	if len(restarted.costumeSettings[100].Sequence) != 0 {
		t.Fatalf("setting was not cleared: %+v", restarted.costumeSettings[100])
	}
}

func TestCostumeSettingInfoIsSorted(t *testing.T) {
	f := newPresetFixture(t)
	for seq, character := range []uint64{200, 100} {
		setting := wire.AppendVarint(nil, 1, character)
		if _, _, _, err := f.deck.Handle("/DeckCostumeSettingSave", req(uint64(seq+1), wire.AppendBytes(nil, 2, setting))); err != nil {
			t.Fatal(err)
		}
	}
	_, response, _, err := f.deck.Handle("/DeckCostumeSettingInfo", req(8))
	if err != nil {
		t.Fatal(err)
	}
	var indices []uint64
	_ = wire.Walk(response, func(field wire.Field) error {
		if field.Number == 1 {
			index, _, _ := wire.Varint(field.Value, 1)
			indices = append(indices, index)
		}
		return nil
	})
	if !sort.SliceIsSorted(indices, func(i, j int) bool { return indices[i] < indices[j] }) || !reflect.DeepEqual(indices, []uint64{100, 200}) {
		t.Fatalf("setting order=%v", indices)
	}
}
