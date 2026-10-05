package world

import (
	"bd2server/internal/server/deck"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
	"testing"
)

func TestMonsterFieldDamagePartyFractionAndRequestReplay(t *testing.T) {
	s := testService()
	store := stateio.NewMemory()
	starter := &player.Starter{Version: versionconfig.State(), Characters: []player.Character{{InvenIndex: 77, ID: 350, Level: 1, HP: 100}, {InvenIndex: 88, ID: 650, Level: 1, HP: 100}}}
	items, e := player.OpenInventory(store, starter)
	if e != nil {
		t.Fatal(e)
	}
	chars, e := player.OpenCharacterStore(store, starter.Characters, items, "", "")
	if e != nil {
		t.Fatal(e)
	}
	chars.AttachMaxHealth(func(player.Character) (uint64, error) { return 100, nil })
	chars.EnsurePersisted()
	chars.SetCurrentHealth(77, 70)
	chars.SetCurrentHealth(88, 0)
	s.characters = chars
	s.decks, e = deck.NewStore(deck.Seed{Version: versionconfig.State(), FieldCharControlDeckType: 1, FieldDeck: []deck.FieldEntry{{Slot: 1, CharacterInvenIndex: 77}, {Slot: 2, CharacterInvenIndex: 88}}})
	if e != nil {
		t.Fatal(e)
	}
	s.fieldBuffs = map[uint64]gamedata.FieldBuffDesign{4: {ID: 4, Type: 5, TargetType: 1, Value: .25}, 5: {ID: 5, Type: 4, TargetType: 0, Value: 10}}
	s.AttachFieldMonsterState(store)
	s.BeginSession("login")
	s.AttachFieldMonsterDamage(s.applyMonsterFieldDamage)
	s.monsterLoader = func(int) ([]gamedata.FieldMonsterDesign, error) {
		return []gamedata.FieldMonsterDesign{{ID: 9, GroupID: 7, Type: 3, FieldBuff: 4}}, nil
	}
	req := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 9)
	_, first, _, e := s.Handle("/FieldMonsterDamage", req)
	if e != nil {
		t.Fatal(e)
	}
	hp, _ := chars.CurrentHealth(77)
	dead, _ := chars.CurrentHealth(88)
	if hp != 45 || dead != 0 {
		t.Fatal("wrong party damage", hp, dead)
	}
	_, again, _, e := s.Handle("/FieldMonsterDamage", req)
	if e != nil || string(first) != string(again) {
		t.Fatal("damage replay response differs", e)
	}
	hp, _ = chars.CurrentHealth(77)
	if hp != 45 {
		t.Fatal("retry damaged again", hp)
	}
	chars.SetCurrentHealth(77, 0)
	rows, e := s.applyMonsterFieldDamage(21, 5, "test")
	if e != nil || len(rows) != 1 {
		t.Fatal("dead leader should remain the controlled leader", e)
	}
	hp, _ = chars.CurrentHealth(88)
	if hp != 0 {
		t.Fatal("dead leader incorrectly switched to another character")
	}
}
