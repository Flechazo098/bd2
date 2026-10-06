package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/progress"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"testing"
)

type eventPackSource struct {
	active bool
	pack   gamedata.EventFieldPack
}

func (p *eventPackSource) ResolveEventFieldPack(id int) (gamedata.EventFieldPack, bool, error) {
	return p.pack, p.active && p.pack.ID == id, nil
}
func (p *eventPackSource) ListEventFieldPacks() ([]gamedata.EventFieldPack, error) {
	if p.active {
		return []gamedata.EventFieldPack{p.pack}, nil
	}
	return nil, nil
}

// Exercise the actual purchase-to-field request chain rather than a single
// permissive route. A hidden scene must not overwrite relogin's outside map.
func TestHiddenPackPurchaseEntryMonstersAndOutsideRestore(t *testing.T) {
	s := testService()
	storage := stateio.NewMemory()
	var err error
	s.state, err = progress.OpenStore(storage)
	if err != nil {
		t.Fatal(err)
	}
	s.collection, err = player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.inventory, err = player.OpenInventory(storage, s.starter)
	if err != nil {
		t.Fatal(err)
	}
	s.wallet, err = player.OpenWallet(storage, player.Currency{Gold: 100})
	if err != nil {
		t.Fatal(err)
	}
	source := &eventPackSource{active: true, pack: gamedata.EventFieldPack{ID: 912, MapIDs: []int{9121}, InitialPosition: "{}", BuyType: 4, BuyPrice: 20}}
	if err = s.AttachEventFieldPacks(source); err != nil {
		t.Fatal(err)
	}
	s.monsterLoader = func(pack int) ([]gamedata.FieldMonsterDesign, error) {
		if pack != 912 {
			t.Fatalf("wrong pack for monsters %d", pack)
		}
		return []gamedata.FieldMonsterDesign{{ID: 3, GroupID: 7}}, nil
	}
	outside := wire.AppendString(wire.AppendVarint(nil, 2, 21), 3, `{"MapId":211,"PlayerPosition":{"x":4}}`)
	if err = s.state.SaveUserPosition(outside); err != nil {
		t.Fatal(err)
	}
	if err = s.state.SetActivePackID(21); err != nil {
		t.Fatal(err)
	}
	r := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 912)
	if _, _, _, err = s.Handle("/PackInGameInfo", r); err == nil {
		t.Fatal("unbought hidden pack admitted")
	}
	for range 2 {
		code, out, ok, err := s.Handle("/PackBuy", r)
		if err != nil || code != 6 || !ok {
			t.Fatalf("buy %d %v", code, err)
		}
		info, found, err := wire.Bytes(out, 1)
		if err != nil || !found {
			t.Fatal("missing PackInfo")
		}
		id, _, _ := wire.Varint(info, 1)
		owned, _, _ := wire.Varint(info, 8)
		if id != 912 || owned != 1 {
			t.Fatal("hidden buy response missing purchased identity")
		}
	}
	if s.wallet.Snapshot().Gold != 80 {
		t.Fatal("replayed purchase spent twice")
	}
	if _, _, _, err = s.Handle("/FieldObjectInfo", r); err != nil {
		t.Fatal(err)
	}
	_, out, _, err := s.Handle("/PackInGameInfo", r)
	if err != nil {
		t.Fatal(err)
	}
	position, _, _ := wire.Bytes(out, 4)
	if string(position) != "{}" {
		t.Fatal("outside position leaked into hidden scene")
	}
	if _, found, _ := wire.Bytes(out, 12); !found {
		t.Fatal("client HuntingGroundInfo null")
	}
	if _, found, _ := wire.Bytes(out, 2); found {
		t.Fatal("hidden scene inherited story quests")
	}
	monster := wire.AppendVarint(wire.AppendVarint(nil, 1, 3), 2, 7)
	code, out, _, err := s.Handle("/MonsterInfo", monster)
	if err != nil || code != 51 {
		t.Fatalf("monster info %v", err)
	}
	m, _, _ := wire.Bytes(out, 1)
	id, _, _ := wire.Varint(m, 1)
	if id != 3 {
		t.Fatal("hidden monster not generated")
	}
	if pack, err := s.LastPlayedPackID(); err != nil || pack != 21 {
		t.Fatalf("hidden scene replaced persistent login %d %v", pack, err)
	}
	source.active = false
	if _, _, _, err = s.Handle("/PackBuy", r); err == nil {
		t.Fatal("expired pack repurchased")
	}
	if _, _, _, err = s.Handle("/PackInGameInfo", r); err == nil {
		t.Fatal("expired pack entered")
	}
	if pack, err := s.LastPlayedPackID(); err != nil || pack != 21 {
		t.Fatalf("expired hidden scene locked login %d %v", pack, err)
	}
}
