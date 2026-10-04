package readonly

import (
	"path/filepath"
	"testing"

	"bd2server/internal/server/wire"
)

func TestRootSortUsesVersionedServerOrdering(t *testing.T) {
	seed := &Seed{Version: StateVersion(), Responses: map[string]Response{"/RootSortIdInfo": {PacketCode: 367, Fields: []Field{
		{Number: 1, Type: 2, Fields: []Field{{Number: 1, Type: 0, Varint: 4}, {Number: 2, Type: 0, Varint: 907}, {Number: 3, Type: 0, Varint: 8}}},
	}}}}
	path := filepath.Join(t.TempDir(), "readonly.json")
	if err := seed.Write(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	code, payload, handled, err := loaded.Handle("/RootSortIdInfo", wire.AppendVarint(nil, 1, 1))
	if err != nil || !handled || code != 367 {
		t.Fatalf("response: %d %v %v", code, handled, err)
	}
	entry, _, err := wire.Bytes(payload, 1)
	id, _, _ := wire.Varint(entry, 2)
	order, _, _ := wire.Varint(entry, 3)
	if err != nil || id != 907 || order != 8 {
		t.Fatalf("ordering id=%d sort=%d err=%v", id, order, err)
	}
}
