package player

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/wire"
	"path/filepath"
	"reflect"
	"testing"
)

// EquipInfo is the client's complete reconstruction source after it clears
// both equipment dictionaries during relogin. Exercise real SQLite writes and
// reopen rather than only the item serializer or an in-memory snapshot.
func TestEquipmentReconnectRestoresEveryCharacterAndClearedReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	open := func(repo *accountstate.Repository) *EquipmentInventory {
		items, e := OpenInventory(repo, &Starter{Version: "2.35.10"})
		if e != nil {
			t.Fatal(e)
		}
		chars, e := OpenCharacterStore(repo, []Character{{InvenIndex: 77, ID: 350, Level: 1, HP: 100}, {InvenIndex: 78, ID: 360, Level: 1, HP: 100}}, items, "", "")
		if e != nil {
			t.Fatal(e)
		}
		if err := chars.AttachMaxHealth(func(Character) (uint64, error) { return 100, nil }); err != nil {
			t.Fatal(err)
		}
		if e = chars.EnsurePersisted(); e != nil {
			t.Fatal(e)
		}
		eq, e := OpenEquipmentInventory(repo)
		if e != nil {
			t.Fatal(e)
		}
		if e = eq.AttachCharacters(chars); e != nil {
			t.Fatal(e)
		}
		if e = eq.AttachSlots(map[uint64]uint64{100: 0, 101: 0, 102: 1, 103: 0}); e != nil {
			t.Fatal(e)
		}
		return eq
	}
	eq := open(repo)
	eq.BeginSession("before-restart")
	var entries []Equipment
	for i, id := range []uint64{100, 101, 102, 103} {
		entry, e := eq.GrantOnce("grant"+string(rune('a'+i)), id)
		if e != nil {
			t.Fatal(e)
		}
		entries = append(entries, entry)
	}
	request := func(seq, equip, char uint64) []byte {
		return wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, seq), 2, equip), 3, char)
	}
	for _, r := range []struct {
		path             string
		seq, index, char uint64
	}{{"/EquipUse", 1, entries[0].InvenIndex, 77}, {"/EquipUse", 2, entries[2].InvenIndex, 77}, {"/EquipUse", 3, entries[3].InvenIndex, 78}, {"/EquipChange", 4, entries[1].InvenIndex, 77}} {
		op, e := repo.BeginOperation()
		if e != nil {
			t.Fatal(e)
		}
		if _, _, _, e = eq.Handle(r.path, request(r.seq, r.index, r.char)); e != nil {
			t.Fatal(e)
		}
		if e = op.Commit(); e != nil {
			t.Fatal(e)
		}
	}
	want := map[uint64]uint64{entries[0].InvenIndex: 0, entries[1].InvenIndex: 77, entries[2].InvenIndex: 77, entries[3].InvenIndex: 78}
	if err = repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	eq = open(repo)
	eq.BeginSession("after-restart")
	code, response, handled, err := eq.Handle("/EquipInfo", wire.AppendVarint(nil, 1, 1))
	if err != nil || !handled || code != 34 {
		t.Fatal(err)
	}
	got := map[uint64]uint64{}
	err = wire.Walk(response, func(f wire.Field) error {
		if f.Number != 1 {
			return nil
		}
		index, _, e := wire.Varint(f.Value, 1)
		if e != nil {
			return e
		}
		owner, _, e := wire.Varint(f.Value, 2)
		got[index] = owner
		return e
	})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("reconnect ownership got=%v want=%v error=%v", got, want, err)
	}
	for _, entry := range eq.All() {
		if entry.UseChar != want[entry.InvenIndex] {
			t.Fatal("serialized ownership differs from saved equipment")
		}
	}
}
