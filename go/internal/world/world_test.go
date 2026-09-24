package world

import (
	"bytes"
	"encoding/binary"
	"testing"

	"bd2server/internal/deck"
	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/progress"
	"bd2server/internal/stateio"
	"bd2server/internal/wire"
)

func testService() *Service {
	return &Service{seed: Seed{Version: "2.34.13", PackID: 21, StartQuestID: 1}, state: progress.NewStore(), starter: &player.Starter{Version: "2.34.13"}, quests: map[int]gamedata.QuestDesign{1: {ID: 1}, 2: {ID: 2}, 3: {ID: 3}}}
}

func TestQuest29EchoesCurrentStoryDeck(t *testing.T) {
	state := progress.NewStore()
	quests := make(map[int]gamedata.QuestDesign)
	for id := 1; id <= 30; id++ {
		quests[id] = gamedata.QuestDesign{ID: id}
		if id < 29 {
			if err := state.ClearQuest(id, 21); err != nil {
				t.Fatal(err)
			}
		}
	}
	decks, err := deck.NewStore(deck.Seed{Version: "2.34.13", FieldDeck: []deck.FieldEntry{
		{Slot: 1, CharacterInvenIndex: 535607162},
	}})
	if err != nil {
		t.Fatal(err)
	}
	save := wire.AppendVarint(nil, 1, 1)
	want := []uint64{535607162, 535607159, 535607160, 535607161, 535604120}
	for i, index := range want {
		entry := wire.AppendVarint(nil, 1, index)
		entry = wire.AppendVarint(entry, 2, uint64(i+1))
		entry = wire.AppendVarint(entry, 3, uint64(i+1))
		save = wire.AppendBytes(save, 2, entry)
	}
	if _, _, ok, err := decks.Handle("/DeckSave", save); err != nil || !ok {
		t.Fatalf("deck save ok=%v err=%v", ok, err)
	}
	s := &Service{seed: Seed{Version: "2.34.13", PackID: 21, StartQuestID: 1}, state: state,
		starter: &player.Starter{Version: "2.34.13"}, quests: quests, decks: decks}
	request := wire.AppendVarint(nil, 1, 2)
	request = wire.AppendVarint(request, 2, 29)
	request = wire.AppendVarint(request, 3, 21)
	_, response, ok, err := s.Handle("/QuestClear", request)
	if err != nil || !ok {
		t.Fatalf("quest clear ok=%v err=%v", ok, err)
	}
	var got []uint64
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number == 4 {
			index, _, _ := wire.Varint(field.Value, 1)
			got = append(got, index)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("deck rows=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("deck=%v want=%v", got, want)
		}
	}
}

func TestInitialPackInfoMatchesVersionedProtocol(t *testing.T) {
	s := testService()
	request := wire.AppendVarint(nil, 1, 89)
	request = wire.AppendVarint(request, 2, 21)
	code, got, ok, err := s.Handle("/PackInGameInfo", request)
	if err != nil || !ok || code != 5 {
		t.Fatalf("Handle: code=%d ok=%v err=%v", code, ok, err)
	}
	want := []byte{0x12, 4, 8, 1, 0x30, 0x15, 0x22, 2, '{', '}', 0x4a, 4, 8, 1, 0x10, 1, 0x62, 2, 0x28, 0x15, 0x72, 6, 8, 3, 0x10, 0x4d, 0x18, 1, 0x82, 1, 0x0e, 0x0a, 5, 0x18, 3, 0x20, 0x96, 1, 0x32, 5, 0x18, 3, 0x20, 0x96, 1}
	if !bytes.Equal(got, want) {
		t.Fatalf("pack proto mismatch\ngot  %x\nwant %x", got, want)
	}
}

func TestQuestClearAdvancesAndPersists(t *testing.T) {
	s := testService()
	request := wire.AppendVarint(nil, 1, 107)
	request = wire.AppendVarint(request, 2, 1)
	request = wire.AppendVarint(request, 3, 21)
	code, response, ok, err := s.Handle("/QuestClear", request)
	if err != nil || !ok || code != 18 {
		t.Fatalf("first clear: code=%d ok=%v err=%v", code, ok, err)
	}
	cleared, found, err := wire.Varint(response, 3)
	if err != nil || !found || cleared != 1 {
		t.Fatalf("clear echo: %d %v %v", cleared, found, err)
	}
	next, found, err := wire.Bytes(response, 2)
	if err != nil || !found {
		t.Fatalf("next: %v", err)
	}
	id, found, err := wire.Varint(next, 1)
	if err != nil || !found || id != 2 {
		t.Fatalf("next id=%d found=%v err=%v", id, found, err)
	}
	if !s.state.QuestCleared(1, 21) {
		t.Fatal("clear was not stored")
	}
	var deckRows int
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number == 4 {
			deckRows++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if deckRows != 0 {
		t.Fatalf("ordinary quest clear unexpectedly replaced deck with %d rows", deckRows)
	}
	packRequest := wire.AppendVarint(nil, 1, 200)
	packRequest = wire.AppendVarint(packRequest, 2, 21)
	_, restored, _, err := s.Handle("/PackInGameInfo", packRequest)
	if err != nil {
		t.Fatal(err)
	}
	active, found, err := wire.Bytes(restored, 2)
	if err != nil || !found {
		t.Fatalf("restored active quest missing: %v", err)
	}
	activeID, found, err := wire.Varint(active, 1)
	if err != nil || !found || activeID != 2 {
		t.Fatalf("restored active quest=%d found=%v err=%v", activeID, found, err)
	}
	clearedIDs, found, err := wire.Bytes(restored, 3)
	clearedID, count := binary.Uvarint(clearedIDs)
	if err != nil || !found || count <= 0 || clearedID != 1 {
		t.Fatalf("restored cleared quests=%x found=%v err=%v", clearedIDs, found, err)
	}
	request = wire.AppendVarint(nil, 1, 108)
	request = wire.AppendVarint(request, 2, 3)
	request = wire.AppendVarint(request, 3, 21)
	if _, _, _, err := s.Handle("/QuestClear", request); err == nil {
		t.Fatal("out-of-order clear succeeded")
	}
}

func TestFinalQuestClearIncludesEmptyNextQuestInfo(t *testing.T) {
	// In 2.34.13 the completion coroutine unconditionally evaluates
	// QuestClearResponse.QuestInfo.Id.  An absent protobuf field becomes a
	// null C# reference; an explicitly present zero-length message becomes a
	// non-null QuestDBInfo with ID 0, satisfying the client's final-quest
	// sentinel path. Exact official final-pack wire parity is still unverified.
	s := &Service{
		seed:       Seed{Version: "2.34.13", PackID: 21, StartQuestID: 38},
		state:      progress.NewStore(),
		starter:    &player.Starter{Version: "2.34.13"},
		quests:     map[int]gamedata.QuestDesign{38: {ID: 38}},
		transition: gamedata.PackTransition{PackID: 21, NextPackID: 22},
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, 38)
	request = wire.AppendVarint(request, 3, 21)
	_, response, ok, err := s.Handle("/QuestClear", request)
	if err != nil || !ok {
		t.Fatalf("final clear ok=%v err=%v", ok, err)
	}
	var nextCount int
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		nextCount++
		if len(field.Value) != 0 {
			t.Fatalf("final next QuestInfo=%x, want explicitly empty message", field.Value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if nextCount != 1 {
		t.Fatalf("final clear has %d QuestInfo fields, want one empty message", nextCount)
	}
	var packs []uint64
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number == 11 {
			id, _, _ := wire.Varint(field.Value, 1)
			packs = append(packs, id)
		}
		return nil
	}); err != nil || len(packs) != 2 || packs[0] != 21 || packs[1] != 22 {
		t.Fatalf("final pack transition=%v err=%v", packs, err)
	}
}

func TestPackInfoRestoresCompletedPackAndUnlockedNextPack(t *testing.T) {
	state := progress.NewStore()
	for _, id := range []int{1, 2} {
		if err := state.ClearQuest(id, 21); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{seed: Seed{Version: "2.34.13", PackID: 21}, state: state,
		quests: map[int]gamedata.QuestDesign{1: {ID: 1}, 2: {ID: 2}}, transition: gamedata.PackTransition{PackID: 21, NextPackID: 22}}
	code, response, handled, err := s.Handle("/PackInfo", wire.AppendVarint(nil, 1, 1))
	if err != nil || !handled || code != 4 {
		t.Fatalf("PackInfo code=%d handled=%v err=%v", code, handled, err)
	}
	var packs []uint64
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number == 1 {
			id, _, _ := wire.Varint(field.Value, 1)
			packs = append(packs, id)
			if id == 22 {
				bought, _, _ := wire.Varint(field.Value, 8)
				if bought != 0 {
					t.Fatalf("newly unlocked pack22 is already marked bought")
				}
			}
		}
		return nil
	}); err != nil || len(packs) != 2 || packs[0] != 21 || packs[1] != 22 {
		t.Fatalf("PackInfo packs=%v err=%v", packs, err)
	}
}

func TestPack22InitializesWithIndependentQuestIdentity(t *testing.T) {
	state := progress.NewStore()
	if err := state.ClearQuest(1, 21); err != nil {
		t.Fatal(err)
	}
	pack21 := map[int]gamedata.QuestDesign{1: {ID: 1}}
	pack22 := map[int]gamedata.QuestDesign{1: {ID: 1}, 2: {ID: 2}}
	s := &Service{
		seed:    Seed{Version: "2.34.13", PackID: 21, StartQuestID: 1},
		state:   state,
		starter: &player.Starter{Version: "2.34.13"},
		quests:  pack21,
		packs:   map[int]map[int]gamedata.QuestDesign{21: pack21, 22: pack22},
		transitions: map[int]gamedata.PackTransition{
			21: {PackID: 21, NextPackID: 22},
			22: {PackID: 22},
		},
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, 22)
	code, response, handled, err := s.Handle("/PackInGameInfo", request)
	if err != nil || !handled || code != 5 {
		t.Fatalf("pack22 init code=%d handled=%v err=%v", code, handled, err)
	}
	currentPack, err := s.CurrentPackID()
	if err != nil || currentPack != 22 {
		t.Fatalf("current pack after pack22 init=%d err=%v", currentPack, err)
	}
	active, found, err := wire.Bytes(response, 2)
	if err != nil || !found {
		t.Fatalf("pack22 active quest missing: %v", err)
	}
	id, _, _ := wire.Varint(active, 1)
	packID, _, _ := wire.Varint(active, 6)
	if id != 1 || packID != 22 {
		t.Fatalf("pack22 active quest id=%d pack=%d", id, packID)
	}
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number == 1 || field.Number == 9 || field.Number == 14 || field.Number == 16 {
			t.Fatalf("pack22 inherited pack21-only field %d", field.Number)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	clear := wire.AppendVarint(nil, 1, 2)
	clear = wire.AppendVarint(clear, 2, 1)
	clear = wire.AppendVarint(clear, 3, 22)
	if _, _, _, err := s.Handle("/QuestClear", clear); err != nil {
		t.Fatal(err)
	}
	if !state.QuestCleared(1, 21) || !state.QuestCleared(1, 22) {
		t.Fatal("quest 1 did not remain independently cleared in pack21 and pack22")
	}
}

func TestQuest28GrantsEquipmentInRewardBundle(t *testing.T) {
	state := progress.NewStore()
	quests := make(map[int]gamedata.QuestDesign)
	for id := 1; id <= 29; id++ {
		quests[id] = gamedata.QuestDesign{ID: id}
		if id < 28 {
			if err := state.ClearQuest(id, 21); err != nil {
				t.Fatal(err)
			}
		}
	}
	storage := stateio.NewMemory()
	equipment, err := player.OpenEquipmentInventory(storage)
	if err != nil {
		t.Fatal(err)
	}
	starter := &player.Starter{Version: "2.34.13"}
	inventory, err := player.OpenInventory(storage, starter)
	if err != nil {
		t.Fatal(err)
	}
	entry := quests[28]
	entry.Rewards[0] = []gamedata.Reward{{Type: 3, Count: 70}, {Type: 10, ID: 10010}}
	quests[28] = entry
	s := &Service{seed: Seed{Version: "2.34.13", PackID: 21, StartQuestID: 1}, state: state,
		starter: starter, equipment: equipment, inventory: inventory, quests: quests}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, 28)
	request = wire.AppendVarint(request, 3, 21)
	code, response, ok, err := s.Handle("/QuestClear", request)
	if err != nil || !ok || code != 18 {
		t.Fatalf("clear: code=%d ok=%v err=%v", code, ok, err)
	}
	bundle, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("reward bundle: %v", err)
	}
	encoded, found, err := wire.Bytes(bundle, 4)
	if err != nil || !found {
		t.Fatalf("equipment reward: %v", err)
	}
	base, found, err := wire.Bytes(encoded, 5)
	if err != nil || !found {
		t.Fatalf("equipment base: %v", err)
	}
	id, found, err := wire.Varint(base, 1)
	if err != nil || !found || id != 10010 {
		t.Fatalf("equipment id=%d found=%v err=%v", id, found, err)
	}
}

func TestQuest27UsesGameDataFreeJewelryReward(t *testing.T) {
	storage := stateio.NewMemory()
	state := progress.NewStore()
	quests := make(map[int]gamedata.QuestDesign)
	for id := 1; id <= 27; id++ {
		quests[id] = gamedata.QuestDesign{ID: id}
		if id < 27 {
			if err := state.ClearQuest(id, 21); err != nil {
				t.Fatal(err)
			}
		}
	}
	design := quests[27]
	design.Rewards[0] = []gamedata.Reward{{Type: 3, Count: 70}}
	quests[27] = design
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{seed: Seed{Version: "2.34.13", PackID: 21, StartQuestID: 1}, state: state,
		starter: &player.Starter{Version: "2.34.13"}, wallet: wallet, quests: quests}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, 27)
	request = wire.AppendVarint(request, 3, 21)
	_, response, _, err := s.Handle("/QuestClear", request)
	if err != nil {
		t.Fatal(err)
	}
	if got := wallet.Snapshot().FreeJewelry; got != 70 {
		t.Fatalf("free jewelry=%d", got)
	}
	bundle, _, _ := wire.Bytes(response, 1)
	item, _, _ := wire.Bytes(bundle, 1)
	typ, _, _ := wire.Varint(item, 3)
	count, _, _ := wire.Varint(item, 4)
	if typ != 3 || count != 70 {
		t.Fatalf("currency type=%d count=%d", typ, count)
	}
}

func TestPackInfoUsesPersistedCharacterLevel(t *testing.T) {
	storage := stateio.NewMemory()
	starter := &player.Starter{Version: "2.34.13"}
	inventory, err := player.OpenInventory(storage, starter)
	if err != nil {
		t.Fatal(err)
	}
	characters, err := player.OpenCharacterStore(storage,
		[]player.Character{{InvenIndex: 77, ID: 350, Level: 20}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	state := progress.NewStore()
	if err := state.ClearQuest(26, 21); err != nil {
		t.Fatal(err)
	}
	s := &Service{seed: Seed{PackID: 21, BattleUnlockQuestID: 26, RewardCharacter: player.Character{InvenIndex: 77, ID: 350, Level: 1}}, state: state, starter: starter, characters: characters}
	response, err := s.packInfo()
	if err != nil {
		t.Fatal(err)
	}
	var level uint64
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number != 1 {
			return nil
		}
		index, _, _ := wire.Varint(field.Value, 1)
		if index == 77 {
			level, _, _ = wire.Varint(field.Value, 4)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if level != 20 {
		t.Fatalf("pack character level=%d want20", level)
	}
}
