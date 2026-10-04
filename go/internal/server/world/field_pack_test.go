package world

import (
	"testing"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/progress"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func TestArenaPositionRestoresAcrossRestartWithoutStoryQuestOrRepurchase(t *testing.T) {
	storage := stateio.NewMemory()
	state, err := progress.OpenStore(storage)
	if err != nil {
		t.Fatal(err)
	}
	const raw = `{"MapId":30011,"PlayerPosition":{"x":-2.1,"y":0,"z":6.4}}`
	request := wire.AppendVarint(nil, 2, 3001)
	request = wire.AppendString(request, 3, raw)
	if err := state.SaveUserPosition(request); err != nil {
		t.Fatal(err)
	}
	state, err = progress.OpenStore(storage)
	if err != nil {
		t.Fatal(err)
	}
	s := testService()
	s.state = state
	s.fieldPacks = map[int]gamedata.FieldPack{3001: {ID: 3001, Type: 3, BuyPrice: 99, UseSchedule: 1, HasOpenRule: true, TicketID: 13001, SquadLevel: 50, MapIDs: map[int]bool{30011: true}}}
	if pack, err := s.LastPlayedPackID(); err != nil || pack != 3001 {
		t.Fatalf("restored login pack=%d err=%v", pack, err)
	}
	s.fieldPacks[3002] = gamedata.FieldPack{ID: 3002, Type: 3, MapIDs: map[int]bool{30021: true}}
	accountInfo := s.accountPackInfo()
	arenaRows := 0
	if err := wire.Walk(accountInfo, func(field wire.Field) error {
		if field.Number == 1 {
			id, _, _ := wire.Varint(field.Value, 1)
			if id == 3002 {
				t.Fatal("unvisited arena was exposed as purchased")
			}
			if id == 3001 {
				arenaRows++
				buy, _, _ := wire.Varint(field.Value, 8)
				if buy != 1 {
					t.Fatal("saved arena missing purchased marker")
				}
				if err := wire.Walk(field.Value, func(inner wire.Field) error {
					if inner.Number != 1 && inner.Number != 8 {
						t.Fatalf("saved arena fabricated field %d", inner.Number)
					}
					return nil
				}); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if arenaRows != 1 {
		t.Fatalf("saved arena rows=%d", arenaRows)
	}
	request = wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 3001)
	code, body, handled, err := s.Handle("/PackInGameInfo", request)
	if err != nil || !handled || code != 5 {
		t.Fatalf("arena info code=%d handled=%v err=%v", code, handled, err)
	}
	position, _, err := wire.Bytes(body, 4)
	if err != nil || string(position) != raw {
		t.Fatalf("restored arena position=%s err=%v", position, err)
	}
	if _, found, _ := wire.Bytes(body, 2); found {
		t.Fatal("arena inherited a story quest")
	}
	if pack, err := s.CurrentPackID(); err != nil || pack != 3001 {
		t.Fatalf("current arena pack=%d err=%v", pack, err)
	}
	request = wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, 21)
	_, body, _, err = s.Handle("/PackInGameInfo", request)
	if err != nil {
		t.Fatal(err)
	}
	position, _, _ = wire.Bytes(body, 4)
	if string(position) != "{}" {
		t.Fatal("arena position leaked into story pack")
	}
}

func TestArenaEntryPreservesTicketPurchaseAndMapRestrictions(t *testing.T) {
	s := testService()
	s.fieldPacks = map[int]gamedata.FieldPack{9001: {ID: 9001, Type: 3, HasOpenRule: true, TicketID: 555, MapIDs: map[int]bool{90011: true}}}
	inventory, err := player.OpenInventory(stateio.NewMemory(), &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	s.inventory = inventory
	if s.packUnlocked(9001) {
		t.Fatal("missing content ticket accepted")
	}
	if _, err := inventory.GrantOnce("arena-ticket", []gamedata.BattleReward{{Type: 19, ID: 555, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	if !s.packUnlocked(9001) {
		t.Fatal("free arena with owned ticket refused")
	}
	pack := s.fieldPacks[9001]
	pack.BuyPrice = 10
	s.fieldPacks[9001] = pack
	if s.packUnlocked(9001) {
		t.Fatal("paid arena was purchased implicitly")
	}
	request := wire.AppendVarint(nil, 2, 9001)
	request = wire.AppendString(request, 3, `{"MapId":211,"PlayerPosition":{"x":1}}`)
	if err := s.state.SaveUserPosition(request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LastPlayedPackID(); err == nil {
		t.Fatal("foreign map restored as arena")
	}
}

func TestLastPlayedStoryPackRestoresItsExactPosition(t *testing.T) {
	s := testService()
	s.packs = map[int]map[int]gamedata.QuestDesign{21: s.quests, 22: {1: {ID: 1}}}
	s.transitions = map[int]gamedata.PackTransition{21: {PackID: 21, NextPackID: 22}}
	for quest := range s.quests {
		if err := s.state.ClearQuest(quest, 21); err != nil {
			t.Fatal(err)
		}
	}
	const raw = `{"MapId":221,"PlayerPosition":{"x":7.5,"y":0,"z":-2.2},"ColleaguePositions":null}`
	request := wire.AppendVarint(nil, 2, 22)
	request = wire.AppendString(request, 3, raw)
	if err := s.state.SaveUserPosition(request); err != nil {
		t.Fatal(err)
	}
	if pack, err := s.LastPlayedPackID(); err != nil || pack != 22 {
		t.Fatalf("last story pack=%d err=%v", pack, err)
	}
	request = wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 22)
	_, body, _, err := s.Handle("/PackInGameInfo", request)
	if err != nil {
		t.Fatal(err)
	}
	position, _, _ := wire.Bytes(body, 4)
	if string(position) != raw {
		t.Fatalf("story position=%s", position)
	}
}
