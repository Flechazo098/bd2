package player

import (
	"path/filepath"
	"reflect"
	"testing"

	"bd2server/internal/gamedata"
	"bd2server/internal/wire"
)

func TestEquipmentOptionRerollLocksChargesReplaysAndRecoversPending(t *testing.T) {
	fixture := newEquipmentOptionRerollFixture(t)
	fixture.equipment.BeginSession("option-reroll-session")

	request := fixture.rerollRequest(41, []bool{false, true}, []bool{false, true, false})
	if rerollType, found, err := wire.Varint(request, 6); err != nil || !found || rerollType != 1 {
		t.Fatalf("malformed test reroll type=%d found=%v err=%v request=%x", rerollType, found, err, request)
	}
	code, response, handled, err := fixture.equipment.Handle("/EquipOptionReRoll", request)
	if err != nil || !handled || code != 192 {
		t.Fatalf("reroll code=%d handled=%v err=%v", code, handled, err)
	}
	wantMain := []EquipmentOption{{GroupID: 100, ID: 1}, {GroupID: 101, ID: 2}}
	wantSub := []EquipmentOption{{GroupID: 200, ID: 11}, {GroupID: 201, ID: 20}, {GroupID: 202, ID: 31}}
	main, sub := optionRerollResponseOptions(t, response)
	if !reflect.DeepEqual(main, wantMain) || !reflect.DeepEqual(sub, wantSub) {
		t.Fatalf("candidate main=%+v sub=%+v", main, sub)
	}
	assertOptionRerollBalances(t, fixture, 800, 13)

	// The candidate is not official equipment until the second-phase confirm.
	current := fixture.equipment.All()[0]
	if !reflect.DeepEqual(current.MainOption, fixture.original.MainOption) || !reflect.DeepEqual(current.SubOption, fixture.original.SubOption) {
		t.Fatalf("reroll changed official equipment before confirm: %+v", current)
	}

	// Same session and sequence must replay exactly, without charging or rolling again.
	replayCode, replay, replayHandled, replayErr := fixture.equipment.Handle("/EquipOptionReRoll", request)
	if replayErr != nil || !replayHandled || replayCode != code || string(replay) != string(response) {
		t.Fatalf("reroll replay code=%d handled=%v same=%v err=%v", replayCode, replayHandled, string(replay) == string(response), replayErr)
	}
	assertOptionRerollBalances(t, fixture, 800, 13)

	_, info, infoHandled, infoErr := fixture.equipment.Handle("/EquipInfo", wire.AppendVarint(nil, 1, 42))
	if infoErr != nil || !infoHandled {
		t.Fatalf("EquipInfo handled=%v err=%v", infoHandled, infoErr)
	}
	pending, found, err := wire.Bytes(info, 2)
	if err != nil || !found {
		t.Fatalf("EquipInfo missing pending option reroll: found=%v err=%v", found, err)
	}
	index, pendingMain, pendingSub := equipmentWireOptions(t, pending)
	if index != fixture.original.InvenIndex || !reflect.DeepEqual(pendingMain, wantMain) || !reflect.DeepEqual(pendingSub, wantSub) {
		t.Fatalf("EquipInfo pending index=%d main=%+v sub=%+v", index, pendingMain, pendingSub)
	}

	// Pending candidates are server state, not a transient UI cache. Reopening the
	// equipment domain must preserve field 2 until the player resolves it.
	restarted, err := OpenEquipmentInventory(testStore(fixture.equipmentPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.AttachOptionReroll(fixture.design, fixture.wallet, fixture.inventory); err != nil {
		t.Fatal(err)
	}
	restarted.BeginSession("option-reroll-after-restart")
	_, restartedInfo, _, err := restarted.Handle("/EquipInfo", wire.AppendVarint(nil, 1, 43))
	if err != nil {
		t.Fatal(err)
	}
	restoredPending, found, err := wire.Bytes(restartedInfo, 2)
	if err != nil || !found || string(restoredPending) != string(pending) {
		t.Fatalf("pending did not survive restart: found=%v same=%v err=%v", found, string(restoredPending) == string(pending), err)
	}

	confirm := optionRerollConfirmRequest(44, fixture.original.InvenIndex, true)
	confirmCode, confirmResponse, confirmHandled, confirmErr := restarted.Handle("/EquipOptionReRollConfirm", confirm)
	if confirmErr != nil || !confirmHandled || confirmCode != 193 {
		t.Fatalf("confirm code=%d handled=%v err=%v", confirmCode, confirmHandled, confirmErr)
	}
	confirmedWire, found, err := wire.Bytes(confirmResponse, 1)
	if err != nil || !found {
		t.Fatalf("confirm response missing equipment: found=%v err=%v", found, err)
	}
	_, confirmedMain, confirmedSub := equipmentWireOptions(t, confirmedWire)
	if !reflect.DeepEqual(confirmedMain, wantMain) || !reflect.DeepEqual(confirmedSub, wantSub) {
		t.Fatalf("confirmed main=%+v sub=%+v", confirmedMain, confirmedSub)
	}
	confirmed := restarted.All()[0]
	if !reflect.DeepEqual(confirmed.MainOption, wantMain) || !reflect.DeepEqual(confirmed.SubOption, wantSub) {
		t.Fatalf("confirmed equipment=%+v", confirmed)
	}
	_, resolvedInfo, _, err := restarted.Handle("/EquipInfo", wire.AppendVarint(nil, 1, 45))
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := wire.Bytes(resolvedInfo, 2); found {
		t.Fatal("confirmed candidate remained in EquipInfo field 2")
	}
}

func TestEquipmentOptionRerollKeepPreservesOfficialOptions(t *testing.T) {
	fixture := newEquipmentOptionRerollFixture(t)
	fixture.equipment.BeginSession("option-reroll-keep")
	request := fixture.rerollRequest(51, []bool{false, true}, []bool{false, true, false})
	if code, _, handled, err := fixture.equipment.Handle("/EquipOptionReRoll", request); err != nil || !handled || code != 192 {
		t.Fatalf("reroll code=%d handled=%v err=%v", code, handled, err)
	}
	assertOptionRerollBalances(t, fixture, 800, 13)

	keep := optionRerollConfirmRequest(52, fixture.original.InvenIndex, false)
	code, response, handled, err := fixture.equipment.Handle("/EquipOptionReRollConfirm", keep)
	if err != nil || !handled || code != 193 {
		t.Fatalf("keep code=%d handled=%v err=%v", code, handled, err)
	}
	keptWire, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("keep response missing equipment: found=%v err=%v", found, err)
	}
	_, keptMain, keptSub := equipmentWireOptions(t, keptWire)
	if !reflect.DeepEqual(keptMain, fixture.original.MainOption) || !reflect.DeepEqual(keptSub, fixture.original.SubOption) {
		t.Fatalf("keep response main=%+v sub=%+v", keptMain, keptSub)
	}
	current := fixture.equipment.All()[0]
	if !reflect.DeepEqual(current.MainOption, fixture.original.MainOption) || !reflect.DeepEqual(current.SubOption, fixture.original.SubOption) {
		t.Fatalf("keep changed official equipment: %+v", current)
	}
	_, info, _, err := fixture.equipment.Handle("/EquipInfo", wire.AppendVarint(nil, 1, 53))
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := wire.Bytes(info, 2); found {
		t.Fatal("discarded candidate remained in EquipInfo field 2")
	}
}

func TestEquipmentOptionRerollRetryLocksPreviousCandidate(t *testing.T) {
	fixture := newEquipmentOptionRerollFixture(t)
	fixture.equipment.BeginSession("option-reroll-retry")

	first := fixture.rerollRequest(54, []bool{false, false}, []bool{false, false, false})
	if code, _, handled, err := fixture.equipment.Handle("/EquipOptionReRoll", first); err != nil || !handled || code != 192 {
		t.Fatalf("first reroll code=%d handled=%v err=%v", code, handled, err)
	}
	firstCandidate := fixture.equipment.pendingReroll.Equipment
	if firstCandidate.MainOption[1].ID == fixture.original.MainOption[1].ID || firstCandidate.SubOption[1].ID == fixture.original.SubOption[1].ID {
		t.Fatalf("fixture did not change lock targets: first=%+v original=%+v", firstCandidate, fixture.original)
	}

	// The result screen sends only masks on a retry. Locked values must come
	// from the first candidate even though it has not been confirmed yet.
	second := fixture.rerollRequest(55, []bool{false, true}, []bool{false, true, false})
	if code, _, handled, err := fixture.equipment.Handle("/EquipOptionReRoll", second); err != nil || !handled || code != 192 {
		t.Fatalf("second reroll code=%d handled=%v err=%v", code, handled, err)
	}
	secondCandidate := fixture.equipment.pendingReroll.Equipment
	if secondCandidate.MainOption[1] != firstCandidate.MainOption[1] || secondCandidate.SubOption[1] != firstCandidate.SubOption[1] {
		t.Fatalf("retry lost locked candidate values: first=%+v second=%+v", firstCandidate, secondCandidate)
	}
	if secondCandidate.MainOption[1] == fixture.original.MainOption[1] || secondCandidate.SubOption[1] == fixture.original.SubOption[1] {
		t.Fatalf("retry restored official values instead of candidate: original=%+v second=%+v", fixture.original, secondCandidate)
	}
}

func TestEquipmentMainOptionChangeUpdatesPendingCandidate(t *testing.T) {
	fixture := newEquipmentOptionRerollFixture(t)
	fixture.equipment.BeginSession("main-option-change")

	first := fixture.rerollRequest(56, []bool{false, false}, []bool{false, false, false})
	if code, _, handled, err := fixture.equipment.Handle("/EquipOptionReRoll", first); err != nil || !handled || code != 192 {
		t.Fatalf("reroll code=%d handled=%v err=%v", code, handled, err)
	}
	if got := fixture.equipment.pendingReroll.Equipment.MainOption[0].ID; got != 1 {
		t.Fatalf("pending first main option=%d want=1 before change", got)
	}

	change := wire.AppendVarint(nil, 1, 57)
	change = wire.AppendVarint(change, 2, fixture.original.InvenIndex)
	change = wire.AppendVarint(change, 3, 100)
	change = wire.AppendVarint(change, 4, 9)
	if code, response, handled, err := fixture.equipment.Handle("/EquipMainOptChange", change); err != nil || !handled || code != 537 || len(response) != 0 {
		t.Fatalf("main option change code=%d response=%x handled=%v err=%v", code, response, handled, err)
	}
	if got := fixture.equipment.All()[0].MainOption[0]; got != (EquipmentOption{GroupID: 100, ID: 9}) {
		t.Fatalf("official first main option=%+v", got)
	}
	if got := fixture.equipment.pendingReroll.Equipment.MainOption[0]; got != (EquipmentOption{GroupID: 100, ID: 9}) {
		t.Fatalf("pending first main option=%+v", got)
	}

	restarted, err := OpenEquipmentInventory(testStore(fixture.equipmentPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.AttachOptionReroll(fixture.design, fixture.wallet, fixture.inventory); err != nil {
		t.Fatal(err)
	}
	if got := restarted.All()[0].MainOption[0]; got != (EquipmentOption{GroupID: 100, ID: 9}) {
		t.Fatalf("restarted official first main option=%+v", got)
	}
	if restarted.pendingReroll == nil || restarted.pendingReroll.Equipment.MainOption[0] != (EquipmentOption{GroupID: 100, ID: 9}) {
		t.Fatalf("restarted pending candidate=%+v", restarted.pendingReroll)
	}

	confirm := optionRerollConfirmRequest(58, fixture.original.InvenIndex, true)
	if code, _, handled, err := restarted.Handle("/EquipOptionReRollConfirm", confirm); err != nil || !handled || code != 193 {
		t.Fatalf("confirm code=%d handled=%v err=%v", code, handled, err)
	}
	if got := restarted.All()[0].MainOption[0]; got != (EquipmentOption{GroupID: 100, ID: 9}) {
		t.Fatalf("confirm reverted changed first main option: %+v", got)
	}
}

func TestEquipmentMainOptionChangeRejectsForgedChoice(t *testing.T) {
	fixture := newEquipmentOptionRerollFixture(t)
	before := fixture.equipment.All()[0]
	request := wire.AppendVarint(nil, 1, 59)
	request = wire.AppendVarint(request, 2, fixture.original.InvenIndex)
	request = wire.AppendVarint(request, 3, 100)
	request = wire.AppendVarint(request, 4, 999)
	if code, _, handled, err := fixture.equipment.Handle("/EquipMainOptChange", request); err == nil || !handled || code != 0 {
		t.Fatalf("forged main option code=%d handled=%v err=%v", code, handled, err)
	}
	if after := fixture.equipment.All()[0]; !reflect.DeepEqual(after, before) {
		t.Fatalf("forged main option changed equipment: before=%+v after=%+v", before, after)
	}
}

func TestEquipmentOptionRerollRejectsForgedMaterialsWithoutCharge(t *testing.T) {
	fixture := newEquipmentOptionRerollFixture(t)
	fixture.equipment.BeginSession("option-reroll-forged-material")

	request := optionRerollRequest(61, fixture.original.InvenIndex,
		[]bool{false, true}, []bool{false, true, false}, 1,
		[]Item{{Type: 4, Count: 1}, {InvenIndex: fixture.material.InvenIndex, Type: 8, ID: 17, Count: 1}})
	if code, _, handled, err := fixture.equipment.Handle("/EquipOptionReRoll", request); err == nil || !handled || code != 0 {
		t.Fatalf("forged materials code=%d handled=%v err=%v", code, handled, err)
	}
	assertOptionRerollBalances(t, fixture, 1000, 20)
	_, info, _, err := fixture.equipment.Handle("/EquipInfo", wire.AppendVarint(nil, 1, 62))
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := wire.Bytes(info, 2); found {
		t.Fatal("rejected forged request created a pending candidate")
	}
}

func TestEquipmentOptionRerollRejectsInvalidLockMasksWithoutCharge(t *testing.T) {
	tests := []struct {
		name string
		main []bool
		sub  []bool
	}{
		{name: "wrong array length", main: []bool{false}, sub: []bool{false, false, false}},
		{name: "first main slot locked", main: []bool{true, false}, sub: []bool{false, false, false}},
		{name: "all rerollable slots locked", main: []bool{false, true}, sub: []bool{true, true, true}},
	}
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newEquipmentOptionRerollFixture(t)
			fixture.equipment.BeginSession("option-reroll-invalid-lock")
			locked := countLockedOptions(test.main) + countLockedOptions(test.sub)
			request := optionRerollRequest(uint64(70+i), fixture.original.InvenIndex, test.main, test.sub, 1,
				fixture.consumeItems(uint64(locked)))
			if code, _, handled, err := fixture.equipment.Handle("/EquipOptionReRoll", request); err == nil || !handled || code != 0 {
				t.Fatalf("invalid locks code=%d handled=%v err=%v", code, handled, err)
			}
			assertOptionRerollBalances(t, fixture, 1000, 20)
		})
	}
}

func TestEquipmentOptionRerollConversionConsumesMixedConcreteStacks(t *testing.T) {
	fixture := newEquipmentOptionRerollFixture(t)
	fixture.equipment.BeginSession("option-reroll-conversion")
	fixture.design.Conversion = &gamedata.EquipmentOptionRerollConversion{
		Ratio: 10, SourceType: 8, SourceID: 16, TargetType: 8, TargetID: 17,
	}
	sources, err := fixture.inventory.GrantOnce("option-reroll-source-material", []gamedata.BattleReward{{Type: 8, ID: 16, Count: 100}})
	if err != nil || len(sources) != 1 {
		t.Fatalf("grant conversion source=%+v err=%v", sources, err)
	}

	// Two locked slots require 7 target units. The client consumes two actual
	// target units plus 50 source units (10 source == one target).
	target := fixture.material
	target.Count = 2
	source := sources[0]
	source.Count = 50
	request := optionRerollRequest(81, fixture.original.InvenIndex,
		[]bool{false, true}, []bool{false, true, false}, 1,
		[]Item{{Type: 4, Count: 200}, target, source})
	if code, _, handled, err := fixture.equipment.Handle("/EquipOptionReRoll", request); err != nil || !handled || code != 192 {
		t.Fatalf("conversion reroll code=%d handled=%v err=%v", code, handled, err)
	}
	if got := fixture.wallet.Snapshot().Gold; got != 800 {
		t.Fatalf("conversion gold=%d want=800", got)
	}
	if got := optionRerollItemCount(fixture.inventory, 8, 17); got != 18 {
		t.Fatalf("target material=%d want=18", got)
	}
	if got := optionRerollItemCount(fixture.inventory, 8, 16); got != 50 {
		t.Fatalf("source material=%d want=50", got)
	}
}

func TestEquipmentOptionRerollConfirmEquippedReturnsCharacter(t *testing.T) {
	fixture := newEquipmentOptionRerollFixture(t)
	character := Character{InvenIndex: 535607162, ID: 350, Level: 20}
	characters, err := OpenCharacterStore(testStore(filepath.Join(filepath.Dir(fixture.equipmentPath), "characters.json")),
		[]Character{character}, fixture.inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.equipment.AttachCharacters(characters); err != nil {
		t.Fatal(err)
	}
	fixture.equipment.mu.Lock()
	next := cloneEquipmentSnapshot(fixture.equipment.owned)
	next.Equipment[0].UseChar = character.InvenIndex
	if err := fixture.equipment.commitLocked(next, "option reroll equipped test setup"); err != nil {
		fixture.equipment.mu.Unlock()
		t.Fatal(err)
	}
	fixture.equipment.mu.Unlock()
	fixture.original = fixture.equipment.All()[0]
	fixture.equipment.BeginSession("option-reroll-equipped")

	request := fixture.rerollRequest(91, []bool{false, true}, []bool{false, true, false})
	if code, _, handled, err := fixture.equipment.Handle("/EquipOptionReRoll", request); err != nil || !handled || code != 192 {
		t.Fatalf("equipped reroll code=%d handled=%v err=%v", code, handled, err)
	}
	confirm := optionRerollConfirmRequest(92, fixture.original.InvenIndex, true)
	code, response, handled, err := fixture.equipment.Handle("/EquipOptionReRollConfirm", confirm)
	if err != nil || !handled || code != 193 {
		t.Fatalf("equipped confirm code=%d handled=%v err=%v", code, handled, err)
	}
	charWire, found, err := wire.Bytes(response, 2)
	if err != nil || !found {
		t.Fatalf("confirm response missing CharDBInfo field 2: found=%v err=%v", found, err)
	}
	charIndex, found, err := wire.Varint(charWire, 1)
	if err != nil || !found || charIndex != character.InvenIndex {
		t.Fatalf("CharDBInfo index=%d want=%d found=%v err=%v", charIndex, character.InvenIndex, found, err)
	}
}

type equipmentOptionRerollFixture struct {
	design        *gamedata.EquipmentOptionRerollDesign
	equipment     *EquipmentInventory
	wallet        *Wallet
	inventory     *Inventory
	original      Equipment
	material      Item
	equipmentPath string
}

func newEquipmentOptionRerollFixture(t *testing.T) *equipmentOptionRerollFixture {
	t.Helper()
	dir := t.TempDir()
	wallet, err := OpenWallet(testStore(filepath.Join(dir, "wallet.json")), Currency{Gold: 1000})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := OpenInventory(testStore(filepath.Join(dir, "items.json")), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	materials, err := inventory.GrantOnce("option-reroll-material", []gamedata.BattleReward{{Type: 8, ID: 17, Count: 20}})
	if err != nil || len(materials) != 1 {
		t.Fatalf("grant option material=%+v err=%v", materials, err)
	}
	equipmentPath := filepath.Join(dir, "equipment.json")
	equipment, err := OpenEquipmentInventory(testStore(equipmentPath))
	if err != nil {
		t.Fatal(err)
	}
	design := optionRerollDomainTestDesign()
	if err := equipment.AttachOptionReroll(design, wallet, inventory); err != nil {
		t.Fatal(err)
	}
	entry, err := equipment.GrantOnce("option-reroll-equipment", 77)
	if err != nil {
		t.Fatal(err)
	}
	equipment.mu.Lock()
	next := cloneEquipmentSnapshot(equipment.owned)
	next.Equipment[0].Level = 9
	next.Equipment[0].MainOption = []EquipmentOption{{GroupID: 100, ID: 1}, {GroupID: 101, ID: 2}}
	next.Equipment[0].SubOption = []EquipmentOption{{GroupID: 200, ID: 10}, {GroupID: 201, ID: 20}, {GroupID: 202, ID: 30}}
	next.Equipment[0].PrivateOption = &EquipmentOption{GroupID: 300, ID: 40}
	next.Equipment[0].Rank = []uint64{4, 4, 4}
	if err := equipment.commitLocked(next, "option reroll test setup"); err != nil {
		equipment.mu.Unlock()
		t.Fatal(err)
	}
	equipment.mu.Unlock()
	original := equipment.All()[0]
	if original.InvenIndex != entry.InvenIndex {
		t.Fatalf("equipment index=%d want=%d", original.InvenIndex, entry.InvenIndex)
	}
	return &equipmentOptionRerollFixture{
		design: design, equipment: equipment, wallet: wallet, inventory: inventory,
		original: original, material: materials[0], equipmentPath: equipmentPath,
	}
}

func optionRerollDomainTestDesign() *gamedata.EquipmentOptionRerollDesign {
	choices := func(oldID, nextID uint64) []gamedata.WeightedOption {
		// A zero-weight old value and one positive new value make the domain test
		// deterministic while retaining two legal choices in every rerollable group.
		return []gamedata.WeightedOption{{ID: oldID, Weight: 0}, {ID: nextID, Weight: 1}}
	}
	return &gamedata.EquipmentOptionRerollDesign{
		Equipment: map[uint64]gamedata.EquipmentOptionRerollItem{77: {
			ID: 77, OptionRerollID: 9, PrivateUniqueCharID: 350,
			MainGroups: []uint64{100, 101}, SubGroups: []uint64{200, 201, 202}, PrivateGroups: []uint64{300},
		}},
		Costs: map[uint64]gamedata.EquipmentOptionRerollCost{9: {Resources: []gamedata.EquipmentOptionRerollResource{
			{Type: 4, BaseCount: 100, LockCount: 50},
			{Type: 8, ID: 17, BaseCount: 3, LockCount: 2},
		}}},
		Groups: map[uint64]gamedata.OptionGroup{
			100: {ID: 100, Choices: choices(1, 9)},
			101: {ID: 101, Choices: choices(2, 3)},
			200: {ID: 200, Choices: choices(10, 11)},
			201: {ID: 201, Choices: choices(20, 21)},
			202: {ID: 202, Choices: choices(30, 31)},
			300: {ID: 300, Choices: choices(40, 41)},
		},
	}
}

func (f *equipmentOptionRerollFixture) consumeItems(locked uint64) []Item {
	material := f.material
	material.Count = 3 + 2*locked
	return []Item{{Type: 4, Count: 100 + 50*locked}, material}
}

func (f *equipmentOptionRerollFixture) rerollRequest(seq uint64, main, sub []bool) []byte {
	locked := countLockedOptions(main) + countLockedOptions(sub)
	return optionRerollRequest(seq, f.original.InvenIndex, main, sub, 1, f.consumeItems(uint64(locked)))
}

func optionRerollRequest(seq, equipmentIndex uint64, main, sub []bool, rerollType uint64, consume []Item) []byte {
	request := wire.AppendVarint(nil, 1, seq)
	request = wire.AppendVarint(request, 2, equipmentIndex)
	request = appendPackedOptionLocks(request, 3, main)
	request = appendPackedOptionLocks(request, 4, sub)
	for _, item := range consume {
		request = wire.AppendBytes(request, 5, ItemWire(item))
	}
	if rerollType != 0 {
		request = wire.AppendVarint(request, 6, rerollType)
	}
	return request
}

func optionRerollConfirmRequest(seq, equipmentIndex uint64, confirm bool) []byte {
	request := wire.AppendVarint(nil, 1, seq)
	request = wire.AppendVarint(request, 2, equipmentIndex)
	if confirm {
		request = wire.AppendVarint(request, 3, 1)
	}
	return request
}

func appendPackedOptionLocks(dst []byte, field int, locks []bool) []byte {
	packed := make([]byte, len(locks))
	for i, locked := range locks {
		if locked {
			packed[i] = 1
		}
	}
	return wire.AppendBytes(dst, field, packed)
}

func countLockedOptions(locks []bool) int {
	count := 0
	for _, locked := range locks {
		if locked {
			count++
		}
	}
	return count
}

func optionRerollResponseOptions(t *testing.T, response []byte) ([]EquipmentOption, []EquipmentOption) {
	t.Helper()
	var main, sub []EquipmentOption
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Type != 2 || (field.Number != 1 && field.Number != 2) {
			return nil
		}
		option := decodeOptionRerollOption(t, field.Value)
		if field.Number == 1 {
			main = append(main, option)
		} else {
			sub = append(sub, option)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return main, sub
}

func equipmentWireOptions(t *testing.T, equipment []byte) (uint64, []EquipmentOption, []EquipmentOption) {
	t.Helper()
	index, found, err := wire.Varint(equipment, 1)
	if err != nil || !found {
		t.Fatalf("equipment wire missing index: found=%v err=%v", found, err)
	}
	base, found, err := wire.Bytes(equipment, 5)
	if err != nil || !found {
		t.Fatalf("equipment wire missing base info: found=%v err=%v", found, err)
	}
	var main, sub []EquipmentOption
	if err := wire.Walk(base, func(field wire.Field) error {
		if field.Type != 2 || (field.Number != 3 && field.Number != 4) {
			return nil
		}
		option := decodeOptionRerollOption(t, field.Value)
		if field.Number == 3 {
			main = append(main, option)
		} else {
			sub = append(sub, option)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return index, main, sub
}

func decodeOptionRerollOption(t *testing.T, data []byte) EquipmentOption {
	t.Helper()
	group, groupFound, err := wire.Varint(data, 1)
	if err != nil || !groupFound {
		t.Fatalf("option missing group: found=%v err=%v", groupFound, err)
	}
	id, idFound, err := wire.Varint(data, 2)
	if err != nil || !idFound {
		t.Fatalf("option missing id: found=%v err=%v", idFound, err)
	}
	return EquipmentOption{GroupID: group, ID: id}
}

func assertOptionRerollBalances(t *testing.T, fixture *equipmentOptionRerollFixture, gold, material uint64) {
	t.Helper()
	if got := fixture.wallet.Snapshot().Gold; got != gold {
		t.Fatalf("gold=%d want=%d", got, gold)
	}
	got := optionRerollItemCount(fixture.inventory, 8, 17)
	if got != material {
		t.Fatalf("option material=%d want=%d", got, material)
	}
}

func optionRerollItemCount(inventory *Inventory, itemType, id uint64) uint64 {
	var count uint64
	for _, item := range inventory.All() {
		if item.Type == itemType && item.ID == id {
			count += item.Count
		}
	}
	return count
}
