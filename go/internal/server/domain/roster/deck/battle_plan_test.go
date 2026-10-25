package deck

import (
	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"bytes"
	"encoding/json"
	"testing"
)

func TestBattlePlanWireRoundTripPreservesNativeSentinels(t *testing.T) {
	want := []BattleAction{
		{CharacterIndex: 101, CostumeIndex: 0},
		{CharacterIndex: 102, CostumeIndex: -1},
		{CharacterIndex: 103, CostumeIndex: 9001, BurstLevel: 2},
	}
	// schema=1; action fields are character=1, costume=2, burst=3.
	// int64 -1 is the native ten-byte varint used for knock-back.
	fixture := []byte{0x12, 4, 8, 101, 16, 0, 0x12, 13, 8, 102, 16,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 1,
		0x12, 7, 8, 103, 16, 0xa9, 0x46, 24, 2, 8, 1}
	if encoded := battlePlanWire(want); !bytes.Equal(encoded, fixture) {
		t.Fatalf("encoded native action plan=%x want %x", encoded, fixture)
	}
	got, err := decodeBattlePlan(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("decoded %d actions, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("action %d = %#v, want %#v", i, got[i], want[i])
		}
	}
	empty, err := decodeBattlePlan(battlePlanWire([]BattleAction{}))
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("explicit empty plan was not preserved: %#v, %v", empty, err)
	}
}

func TestValidateBattlePlanRejectsCharactersOutsidePreset(t *testing.T) {
	s := &Store{}
	preset := Preset{Slot: 1, Decks: []PresetDeck{{Deck: DeckEntry{CharacterInvenIndex: 7}}}}
	if err := s.validateBattlePlan(preset, []BattleAction{{CharacterIndex: 8}}); err == nil {
		t.Fatal("expected action for a character outside the preset to be rejected")
	}
}

func TestValidateBattlePlanRejectsBurstOnDefaultAttack(t *testing.T) {
	s := &Store{}
	preset := Preset{Slot: 1, Decks: []PresetDeck{{Deck: DeckEntry{CharacterInvenIndex: 7}}}}
	if err := s.validateBattlePlan(preset, []BattleAction{{CharacterIndex: 7, BurstLevel: 1}}); err == nil {
		t.Fatal("expected burst on normal attack to be rejected")
	}
}

func TestBattlePlanPersistsByPresetSlotAndReloadsAfterReopen(t *testing.T) {
	mem := stateio.NewMemory()
	ctx := command.Context{State: mem}
	equipment := make([]PresetEquipmentItem, 5)
	for i := range equipment {
		equipment[i].Type = uint64(i)
	}
	preset := Preset{Name: "Plan", Slot: 1, Decks: []PresetDeck{{Deck: DeckEntry{CharacterInvenIndex: 7, CostumeInvenIndex: 1, Slot: 1}, Equipment: equipment}}, BattlePlan: []BattleAction{{CharacterIndex: 7, CostumeIndex: -1}}}
	store := &Store{storage: stateio.RootStore{}, state: state{Version: versionconfig.State(), FieldDeck: []FieldEntry{{CharacterInvenIndex: 1, Slot: 1}}, Waypoints: map[uint64][]uint64{}, Costumes: map[uint64]uint64{}, Packs: map[uint64]uint64{}}}
	if err := store.persistPresetLocked(ctx, preset); err != nil {
		t.Fatal(err)
	}
	if err := store.persistActivePreset(ctx, store.state, 1); err != nil {
		t.Fatal(err)
	}
	seed := Seed{Version: versionconfig.State(), FieldDeck: []FieldEntry{{CharacterInvenIndex: 1, Slot: 1}}}
	reopened, err := OpenStore(ctx, stateio.RootStore{}, seed, gamedata.PresetDesign{BaseCount: 2, Maximum: 2, PriceType: 4, Price: 2000, Icons: map[uint64]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.presets[1].BattlePlan
	if len(got) != 1 || got[0].CharacterIndex != 7 || got[0].CostumeIndex != -1 {
		t.Fatalf("reloaded plan = %#v", got)
	}
	if reopened.activePreset == nil || *reopened.activePreset != 1 {
		t.Fatalf("reloaded active preset = %#v", reopened.activePreset)
	}
	data, found, err := mem.LoadEntry("deck", "active_preset", "slot")
	if err != nil || !found {
		t.Fatalf("active preset missing: found=%v err=%v", found, err)
	}
	var slot uint64
	if err := json.Unmarshal(data, &slot); err != nil || slot != 1 {
		t.Fatalf("active slot=%d err=%v", slot, err)
	}
}

func TestExplicitEmptyBattlePlanIsPersistedAsClear(t *testing.T) {
	mem := stateio.NewMemory()
	ctx := command.Context{State: mem}
	store := &Store{storage: stateio.RootStore{}, state: state{Version: versionconfig.State(), FieldDeck: []FieldEntry{{CharacterInvenIndex: 1, Slot: 1}}, Waypoints: map[uint64][]uint64{}, Costumes: map[uint64]uint64{}, Packs: map[uint64]uint64{}}}
	preset := Preset{Name: "Clear", Slot: 0, BattlePlan: []BattleAction{{CharacterIndex: 7, CostumeIndex: -1}}}
	if err := store.persistPresetLocked(ctx, preset); err != nil {
		t.Fatal(err)
	}
	// A native client that omits field 100 must leave the existing plan alone.
	preset.BattlePlan = nil
	if err := store.persistPresetLocked(ctx, preset); err != nil {
		t.Fatal(err)
	}
	unchanged, found, err := mem.LoadEntry("deck", "battle_plans", "0")
	if err != nil || !found {
		t.Fatalf("omitted plan unexpectedly cleared existing value: found=%v err=%v", found, err)
	}
	var unchangedPlan []BattleAction
	if err := json.Unmarshal(unchanged, &unchangedPlan); err != nil || len(unchangedPlan) != 1 || unchangedPlan[0].CostumeIndex != -1 {
		t.Fatalf("omitted plan changed value: %#v err=%v", unchangedPlan, err)
	}
	preset.BattlePlan = []BattleAction{}
	if err := store.persistPresetLocked(ctx, preset); err != nil {
		t.Fatal(err)
	}
	payload, found, err := mem.LoadEntry("deck", "battle_plans", "0")
	if err != nil || !found {
		t.Fatalf("empty plan was not persisted: found=%v err=%v", found, err)
	}
	var decoded []BattleAction
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded == nil {
		t.Fatalf("stored empty plan=%s err=%v", payload, err)
	}
	seed := Seed{Version: versionconfig.State(), FieldDeck: []FieldEntry{{CharacterInvenIndex: 1, Slot: 1}}}
	storeCore, err := store.corePayloadLocked()
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.Save("deck", storeCore); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(ctx, stateio.RootStore{}, seed, gamedata.PresetDesign{BaseCount: 1, Maximum: 1, PriceType: 4, Price: 2000, Icons: map[uint64]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	if plan := reopened.presets[0].BattlePlan; plan == nil || len(plan) != 0 {
		t.Fatalf("explicit clear did not replace old plan: %#v", plan)
	}
}

func TestBattlePlanWireRejectsInvalidSchemasAndShape(t *testing.T) {
	if _, err := decodeBattlePlan(nil); err == nil {
		t.Fatal("missing schema accepted")
	}
	if _, err := decodeBattlePlan(wire.AppendVarint(nil, 1, 2)); err == nil {
		t.Fatal("unsupported schema accepted")
	}
	s := &Store{}
	preset := Preset{Decks: []PresetDeck{{Deck: DeckEntry{CharacterInvenIndex: 7}}, {Deck: DeckEntry{CharacterInvenIndex: 8}}}}
	for _, plan := range [][]BattleAction{{{CharacterIndex: 7}, {CharacterIndex: 7}}, {{CharacterIndex: 7}, {CharacterIndex: 8}, {CharacterIndex: 9}}, {{CharacterIndex: 7, CostumeIndex: -2}}} {
		if err := s.validateBattlePlan(preset, plan); err == nil {
			t.Fatalf("invalid plan accepted: %#v", plan)
		}
	}
}

func TestBattlePlanRejectsWrongCostumeAndBurst(t *testing.T) {
	mem := stateio.NewMemory()
	ctx := command.Context{State: mem}
	collection, err := roster.OpenCollectionStore(ctx, stateio.RootStore{}, []roster.Costume{{InvenIndex: 90, ID: 9, UseChar: 8, BurstLevel: 1}, {InvenIndex: 91, ID: 10, UseChar: 7, BurstLevel: 1}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{collection: collection}
	preset := Preset{Decks: []PresetDeck{{Deck: DeckEntry{CharacterInvenIndex: 7}}}}
	for _, action := range []BattleAction{{CharacterIndex: 7, CostumeIndex: 90}, {CharacterIndex: 7, CostumeIndex: 91, BurstLevel: 2}} {
		if err := s.validateBattlePlan(preset, []BattleAction{action}); err == nil {
			t.Fatalf("invalid costume action accepted: %#v", action)
		}
	}
}

func TestClipBattlePlanDropsMembersRemovedFromPreset(t *testing.T) {
	preset := Preset{Decks: []PresetDeck{{Deck: DeckEntry{CharacterInvenIndex: 7}}}}
	got := clipBattlePlan(preset, []BattleAction{{CharacterIndex: 7}, {CharacterIndex: 8}})
	if len(got) != 1 || got[0].CharacterIndex != 7 {
		t.Fatalf("clipped plan = %#v", got)
	}
}
