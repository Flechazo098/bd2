package deck

import (
	"bd2server/internal/wire"
	"path/filepath"
	"testing"
)

func seeded(t *testing.T) *Store {
	t.Helper()
	x, e := LoadSeed(filepath.Join("..", "..", "seed", "v2_34_13", "decks.json"))
	if e != nil {
		t.Fatal(e)
	}
	s, e := NewStore(x)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func req(seq uint64, fields ...[]byte) []byte {
	b := wire.AppendVarint(nil, 1, seq)
	for _, f := range fields {
		b = append(b, f...)
	}
	return b
}
func triple(a, b, c uint64) []byte {
	v := wire.AppendVarint(nil, 1, a)
	v = wire.AppendVarint(v, 2, b)
	v = wire.AppendVarint(v, 3, c)
	return wire.AppendBytes(nil, 2, v)
}
func TestFieldDeckSeedAndSave(t *testing.T) {
	s := seeded(t)
	code, b, ok, e := s.Handle("/FieldDeckInfo", req(1))
	if e != nil || !ok || code != 273 {
		t.Fatalf("info %d %t %v", code, ok, e)
	}
	n := 0
	if e = wire.Walk(b, func(f wire.Field) error {
		if f.Number == 1 {
			n++
		}
		return nil
	}); e != nil || n != 5 {
		t.Fatalf("seed field deck: %d %v", n, e)
	}
	// FieldDeckDBInfo is sequence#1, character#2, costume#3.
	field := triple(1, 99, 199)
	code, _, ok, e = s.Handle("/FieldDeckSave", req(2, field))
	if e != nil || !ok || code != 274 {
		t.Fatalf("save: %d %t %v", code, ok, e)
	}
	_, b, _, _ = s.Handle("/FieldDeckInfo", req(3))
	var got uint64
	_ = wire.Walk(b, func(f wire.Field) error {
		if f.Number == 1 {
			got, _, _ = wire.Varint(f.Value, 2)
		}
		return nil
	})
	if got != 99 {
		t.Fatalf("saved char=%d", got)
	}
}
func TestDeckPersistenceAndCommands(t *testing.T) {
	seed, e := LoadSeed(filepath.Join("..", "..", "seed", "v2_34_13", "decks.json"))
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "deck.json")
	s, e := OpenStore(path, seed)
	if e != nil {
		t.Fatal(e)
	}
	code, _, _, e := s.Handle("/DeckSave", req(1, triple(100, 200, 1)))
	if e != nil || code != 10 {
		t.Fatalf("deck save: %d %v", code, e)
	}
	way := wire.AppendVarint(nil, 2, 21)
	way = wire.AppendVarint(way, 3, 1)
	if code, _, _, e = s.Handle("/WaypointSave", req(2, way)); e != nil || code != 32 {
		t.Fatalf("way: %d %v", code, e)
	}
	use := wire.AppendVarint(nil, 1, 200)
	use = wire.AppendVarint(use, 2, 100)
	if code, _, _, e = s.Handle("/CostumeUse", req(3, wire.AppendBytes(nil, 2, use))); e != nil || code != 41 {
		t.Fatalf("use: %d %v", code, e)
	}
	pack := wire.AppendVarint(nil, 2, 21)
	code, b, _, e := s.Handle("/PackBuy", req(4, pack))
	if e != nil || code != 6 {
		t.Fatalf("buy: %d %v", code, e)
	}
	p, _, _ := wire.Bytes(b, 1)
	id, _, _ := wire.Varint(p, 1)
	if id != 21 {
		t.Fatalf("pack response=%d", id)
	}
	reopened, e := OpenStore(path, seed)
	if e != nil {
		t.Fatal(e)
	}
	_, b, _, e = reopened.Handle("/DeckInfo", req(5))
	if e != nil {
		t.Fatal(e)
	}
	entry, _, _ := wire.Bytes(b, 1)
	id, _, _ = wire.Varint(entry, 1)
	if id != 100 {
		t.Fatalf("persist deck=%d", id)
	}
}
func TestRejectsInvalidMutations(t *testing.T) {
	s := seeded(t)
	if _, _, _, e := s.Handle("/DeckSave", req(1, triple(1, 2, 1), triple(3, 4, 1))); e == nil {
		t.Fatal("duplicate slots accepted")
	}
	if _, _, _, e := s.Handle("/WaypointSave", req(1, wire.AppendVarint(nil, 2, 21))); e == nil {
		t.Fatal("waypoint missing id accepted")
	}
}

func TestSaveFieldCharControlDeckTypeAcceptsProtoDefaultBattle(t *testing.T) {
	s := seeded(t)
	// FCCD_BATTLE=0 is omitted by proto3 serialization. This is the exact
	// shape sent when the client leaves story control after the final quest.
	code, _, handled, err := s.Handle("/SaveFieldCharControlDeckType", req(1))
	if err != nil || !handled || code != 288 || s.state.FieldCharControlDeckType != 0 {
		t.Fatalf("battle save code=%d handled=%v type=%d err=%v", code, handled, s.state.FieldCharControlDeckType, err)
	}
	for seq, value := range []uint64{1, 2} {
		request := req(uint64(seq+2), wire.AppendVarint(nil, 2, value))
		if code, _, handled, err = s.Handle("/SaveFieldCharControlDeckType", request); err != nil || !handled || code != 288 || s.state.FieldCharControlDeckType != value {
			t.Fatalf("type %d save code=%d handled=%v stored=%d err=%v", value, code, handled, s.state.FieldCharControlDeckType, err)
		}
	}
	if _, _, handled, err = s.Handle("/SaveFieldCharControlDeckType", req(9, wire.AppendVarint(nil, 2, 3))); !handled || err == nil {
		t.Fatalf("unknown enum accepted handled=%v err=%v", handled, err)
	}
}

func TestTotalBattlePowerKeepsHighest(t *testing.T) {
	s := seeded(t)
	for seq, power := range []uint64{1300, 900} {
		request := req(uint64(seq+1), wire.AppendVarint(nil, 2, power))
		code, response, ok, err := s.Handle("/SaveTotalBattlePower", request)
		if err != nil || !ok || code != 258 {
			t.Fatalf("save power: code=%d ok=%v err=%v", code, ok, err)
		}
		highest, found, err := wire.Varint(response, 1)
		if err != nil || !found || highest != 1300 {
			t.Fatalf("highest=%d found=%v err=%v", highest, found, err)
		}
	}
}

func TestPortraitChangeEchoesAndStoresCostume(t *testing.T) {
	s := seeded(t)
	request := req(1, wire.AppendVarint(nil, 2, 3501))
	code, response, ok, err := s.Handle("/UserPortraitChange", request)
	if err != nil || !ok || code != 75 {
		t.Fatalf("portrait: code=%d ok=%v err=%v", code, ok, err)
	}
	id, found, err := wire.Varint(response, 1)
	if err != nil || !found || id != 3501 || s.state.PortraitCostumeID != 3501 {
		t.Fatalf("portrait response=%d found=%v stored=%d err=%v", id, found, s.state.PortraitCostumeID, err)
	}
}

func TestDeckCharAutoReviveUsesCurrentFormationWithoutInventingRevives(t *testing.T) {
	s := seeded(t)
	if code, _, handled, err := s.Handle("/DeckSave", req(1, triple(535607162, 10, 1), triple(535604120, 1, 2))); code != 10 || !handled || err != nil {
		t.Fatalf("save code=%d handled=%v err=%v", code, handled, err)
	}
	code, response, handled, err := s.Handle("/DeckCharAutoRevive", req(2))
	if err != nil || !handled || code != 373 {
		t.Fatalf("auto revive code=%d handled=%v err=%v", code, handled, err)
	}
	var battle, field, revive int
	if err := wire.Walk(response, func(f wire.Field) error {
		switch f.Number {
		case 1:
			battle++
			if battle == 1 {
				index, _, _ := wire.Varint(f.Value, 1)
				if index != 535607162 {
					t.Errorf("first battle character %d", index)
				}
			}
		case 2:
			field++
		case 3, 5:
			revive++
		}
		return nil
	}); err != nil || battle != 2 || field != 5 || revive != 0 {
		t.Fatalf("formation battle=%d field=%d invented revive=%d err=%v", battle, field, revive, err)
	}
	mode, _, _ := wire.Varint(response, 4)
	catalyst, _, _ := wire.Varint(response, 7)
	if mode != 2 || catalyst != 200 {
		t.Fatalf("official formation mode=%d catalyst=%d", mode, catalyst)
	}
	if _, _, handled, err := s.Handle("/DeckCharAutoRevive", req(3, wire.AppendVarint(nil, 2, 535604120))); !handled || err == nil {
		t.Fatalf("unverified caster incorrectly accepted handled=%v err=%v", handled, err)
	}
	if _, _, handled, err := s.Handle("/DeckCharAutoRevive", nil); !handled || err == nil {
		t.Fatalf("missing sequence incorrectly accepted handled=%v err=%v", handled, err)
	}
}

func TestDeckCharAutoReviveMigratesExistingSaveCatalyst(t *testing.T) {
	seed, err := LoadSeed(filepath.Join("..", "..", "seed", "v2_34_13", "decks.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "deck.json")
	s, err := OpenStore(path, seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Handle("/DeckSave", req(1, triple(88, 12, 1))); err != nil {
		t.Fatal(err)
	}
	// Old snapshots stored no catalyst; opening must restore it from the
	// account seed while leaving all deck and field entries untouched.
	s.state.AutoReviveCatalyst = 0
	if err := s.commit(s.state); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path, seed)
	if err != nil || reopened.state.AutoReviveCatalyst != 200 || reopened.state.Deck[0].CharacterInvenIndex != 88 {
		t.Fatalf("migrated state=%+v err=%v", reopened.state, err)
	}
}
