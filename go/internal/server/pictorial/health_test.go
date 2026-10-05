package pictorial

import (
	"bytes"
	"errors"
	"testing"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func TestMaximumHealthIncludesOwnedEquipmentPotentialAndAwakening(t *testing.T) {
	character := player.Character{InvenIndex: 77, ID: 350, Level: 1}
	s := &Service{Design: &gamedata.PictorialDesign{}, Owned: &ownedState{}}
	s.baseHealth.Store([2]uint64{350, 1}, float64(100))
	s.EquipmentContributions = func(got player.Character) ([]gamedata.StatContribution, error) {
		if got.InvenIndex != character.InvenIndex || got.ID != character.ID || got.Level != character.Level {
			t.Fatalf("wrong stat character: %+v", got)
		}
		return []gamedata.StatContribution{{Stat: gamedata.StatHealth, Flat: 20, Percent: .1}}, nil
	}
	s.PotentialContributions = func(player.Character) ([]gamedata.StatContribution, error) {
		return []gamedata.StatContribution{{Stat: gamedata.StatHealth, Percent: .2}}, nil
	}
	s.AwakeContributions = func(player.Character) ([]gamedata.StatContribution, error) {
		return []gamedata.StatContribution{{Stat: gamedata.StatHealth, Flat: 5}}, nil
	}
	maximum, err := s.MaxHealth(character)
	if err != nil || maximum != 162 {
		t.Fatalf("maximum=%d err=%v, want trunc((100+20+5)*1.3)=162", maximum, err)
	}
	s.EquipmentContributions = func(player.Character) ([]gamedata.StatContribution, error) {
		return nil, errors.New("equipment unavailable")
	}
	if _, err := s.MaxHealth(character); err == nil {
		t.Fatal("ignored equipment stat failure")
	}
}

func TestPotentialConnectionSwitchRecomputesMaximumWithoutHealingOrChangingCostume(t *testing.T) {
	store := stateio.NewMemory()
	items, err := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := player.OpenCharacterStore(store, []player.Character{{InvenIndex: 77, ID: 350, HP: 80, Level: 1, UseCostume: 1, CostumeID: 100}}, items, "", "")
	if err != nil {
		t.Fatal(err)
	}
	collection, err := player.OpenCollectionStore(store, []player.Costume{{InvenIndex: 1, ID: 100, UseChar: 77}, {InvenIndex: 2, ID: 200, UseChar: 77}})
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []uint64{1, 2} {
		if err = collection.ActivateCostumePotential(index, []uint64{1, 2}); err != nil {
			t.Fatal(err)
		}
	}
	wallet, err := player.OpenWallet(store, player.Currency{Gold: 100})
	if err != nil {
		t.Fatal(err)
	}
	d := &gamedata.CostumePotentialDesign{CharacterTypes: map[uint64]uint64{350: 0}, CostumeActive: map[uint64]bool{100: true, 200: true}, CharacterUnique: map[uint64]uint64{350: 9}, CostumeUnique: map[uint64]uint64{100: 9, 200: 9}, Nodes: map[uint64]map[uint64]gamedata.CostumePotentialNode{
		100: {1: {ID: 1, NodeType: 2, StatType: 1, StatValue: 10}, 2: {ID: 2, NodeType: 1, StatType: 1, StatValue: 100}},
		200: {1: {ID: 1, NodeType: 2, StatType: 1, StatValue: 5}, 2: {ID: 2, NodeType: 1, StatType: 1, StatValue: 20}},
	}}
	potential, err := player.NewCostumePotentialService(d, collection, characters, items, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if err = potential.AttachConnectStore(store); err != nil {
		t.Fatal(err)
	}
	potential.BeginSession("health")
	stats := &Service{Design: &gamedata.PictorialDesign{}, Owned: &ownedState{}, PotentialContributions: potential.Contributions}
	stats.baseHealth.Store([2]uint64{350, 1}, float64(100))
	if err = characters.AttachMaxHealth(stats.MaxHealth); err != nil {
		t.Fatal(err)
	}
	request := func(seq, costume uint64) []byte {
		row := wire.AppendVarint(wire.AppendVarint(nil, 1, 77), 2, costume)
		return wire.AppendBytes(wire.AppendVarint(nil, 1, seq), 2, row)
	}
	first := request(1, 100)
	if _, _, _, err = potential.Handle("/CostumePotentialConnect", first); err != nil {
		t.Fatal(err)
	}
	if maximum, err := characters.MaxHealth(77); err != nil || maximum != 215 {
		t.Fatalf("first maximum=%d err=%v", maximum, err)
	}
	if hp, err := characters.CurrentHealth(77); err != nil || hp != 80 {
		t.Fatal("connecting potential healed damage")
	}
	if err = characters.SetCurrentHealth(77, 200); err != nil {
		t.Fatal(err)
	}
	second := request(2, 200)
	code, out, handled, err := potential.Handle("/CostumePotentialConnect", second)
	if err != nil || code != 267 || !handled {
		t.Fatal(err)
	}
	c, ok := characters.Find(77)
	if !ok || c.ConnectPotentialCostume != 200 || c.UseCostume != 1 || c.CostumeID != 100 || c.HP != 135 {
		t.Fatalf("connection changed worn costume or failed HP clamp: %+v", c)
	}
	if maximum, err := characters.MaxHealth(77); err != nil || maximum != 135 {
		t.Fatalf("switched maximum=%d err=%v", maximum, err)
	}
	_, again, _, err := potential.Handle("/CostumePotentialConnect", second)
	if err != nil || !bytes.Equal(out, again) {
		t.Fatal("connection replay changed response")
	}
	if err = characters.SetCurrentHealth(77, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = potential.Handle("/CostumePotentialConnect", request(3, 100)); err != nil {
		t.Fatal(err)
	}
	if hp, err := characters.CurrentHealth(77); err != nil || hp != 0 {
		t.Fatal("potential connection revived dead character")
	}
	if wallet.Snapshot().Gold != 100 || len(items.All()) != 0 {
		t.Fatal("free connection charged materials or currency")
	}
}
