package progress

import (
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"bd2server/internal/wire"
)

func TestSaveUserPosition(t *testing.T) {
	request := wire.AppendVarint(nil, 1, 119)
	request = wire.AppendVarint(request, 2, 21)
	request = wire.AppendString(request, 3, `{"MapId":211,"PlayerPosition":{"x":1.5,"y":0.0,"z":-2.5},"ColleaguePositions":null}`)
	store := NewStore()
	if err := store.SaveUserPosition(request); err != nil {
		t.Fatal(err)
	}
	position, found := store.Position()
	if !found || position.PackID != 21 || position.Position.MapID != 211 || position.Position.PlayerPosition.Z != -2.5 {
		t.Fatalf("unexpected saved position: %+v found=%v", position, found)
	}
}

func TestSaveUserPositionRejectsBadJSON(t *testing.T) {
	request := wire.AppendVarint(nil, 2, 21)
	request = wire.AppendString(request, 3, `{bad`)
	if err := NewStore().SaveUserPosition(request); !errors.Is(err, ErrInvalidPosition) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClearTutorialIsIdempotent(t *testing.T) {
	request := wire.AppendVarint(nil, 1, 116)
	request = wire.AppendVarint(request, 2, 2001)
	store := NewStore()
	if err := store.ClearTutorial(request); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearTutorial(request); err != nil {
		t.Fatal(err)
	}
	if !store.TutorialCleared(2001) {
		t.Fatal("tutorial completion was not retained")
	}
}

func TestTutorialInfoReturnsPersistedClears(t *testing.T) {
	store := NewStore()
	for seq, id := range []uint64{2001, 10030, 15} {
		request := wire.AppendVarint(nil, 1, uint64(seq+1))
		request = wire.AppendVarint(request, 2, id)
		if err := store.ClearTutorial(request); err != nil {
			t.Fatal(err)
		}
	}
	code, response, ok, err := store.Handle("/TutorialInfo", wire.AppendVarint(nil, 1, 99))
	if err != nil || !ok || code != 101 {
		t.Fatalf("info: code=%d ok=%v err=%v", code, ok, err)
	}
	packed, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("clears missing: %v", err)
	}
	var got []uint64
	for len(packed) > 0 {
		value, n := binary.Uvarint(packed)
		if n <= 0 {
			t.Fatal("bad packed tutorials")
		}
		got = append(got, value)
		packed = packed[n:]
	}
	want := []uint64{15, 2001, 10030}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tutorials=%v want=%v", got, want)
	}
}

func TestUpdateQuestPackedValues(t *testing.T) {
	request := wire.AppendVarint(nil, 1, 120)
	request = wire.AppendVarint(request, 2, 12)
	request = wire.AppendVarint(request, 3, 21)
	request = wire.AppendBytes(request, 4, []byte{121, 2}) // packed 121, 2
	store := NewStore()
	updated, err := store.UpdateQuest(request)
	if err != nil {
		t.Fatal(err)
	}
	quest, found := store.Quest(12)
	if updated != 12 || !found || quest.PackID != 21 || len(quest.Values) != 2 || quest.Values[0] != 121 || quest.Values[1] != 2 {
		t.Fatalf("unexpected quest: updated=%d found=%v quest=%+v", updated, found, quest)
	}
}
