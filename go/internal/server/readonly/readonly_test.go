package readonly

import (
	"bytes"
	"path/filepath"
	"testing"

	"bd2server/internal/server/wire"
)

func TestSeedProtocolEncodingAndRoundTrip(t *testing.T) {
	seed := &Seed{Version: StateVersion(), Responses: map[string]Response{
		"/CashShopInfo": {PacketCode: 60, Fields: []Field{
			{Number: 1, Type: 2, Fields: []Field{{Number: 1, Type: 0, Varint: 150}, {Number: 2, Type: 0, Varint: 7}}},
			{Number: 2, Type: 1, Fixed64: 0x0807060504030201},
			{Number: 3, Type: 5, Fixed32: 0x04030201},
			{Number: 4, Type: 2, Bytes: []byte("shop")},
			{Number: 5, Type: 2},
		}},
	}}
	path := filepath.Join(t.TempDir(), "readonly.json")
	if err := seed.Write(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x0a, 5, 0x08, 0x96, 1, 0x10, 7, 0x11, 1, 2, 3, 4, 5, 6, 7, 8, 0x1d, 1, 2, 3, 4, 0x22, 4, 's', 'h', 'o', 'p', 0x2a, 0}
	code, got, handled, err := loaded.Handle("/CashShopInfo", wire.AppendVarint(nil, 1, 999))
	if err != nil || !handled || code != 60 || !bytes.Equal(got, want) {
		t.Fatalf("code=%d proto=%x handled=%v err=%v", code, got, handled, err)
	}
	got[0] = 0
	_, again, _, err := loaded.Handle("/CashShopInfo", wire.AppendVarint(nil, 1, 1000))
	if err != nil || !bytes.Equal(again, want) {
		t.Fatal("encoded response aliases seed data")
	}
	for _, request := range [][]byte{nil, {0x08, 0}, {0x08, 0x80}} {
		if _, _, handled, err := loaded.Handle("/CashShopInfo", request); !handled || err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if _, _, handled, err := loaded.Handle("/Unknown", nil); handled || err != nil {
		t.Fatal("unknown endpoint accepted")
	}
}

func TestCashProductEventIndexIsUniqueSemanticSeedFact(t *testing.T) {
	seed, err := Load(filepath.Join("..", "..", "..", "seed", "v2_35_10", "readonly.json"))
	if err != nil {
		t.Fatal(err)
	}
	event, err := seed.CashProductEventIndex(1100001, 9100033)
	if err != nil || event != 1171 {
		t.Fatalf("event=%d err=%v", event, err)
	}
	if _, err := seed.CashProductEventIndex(1100001, 999); err == nil {
		t.Fatal("missing cash product event was accepted")
	}
	duplicate := seed.Responses["/CashShopInfo"]
	for _, field := range duplicate.Fields {
		var group, product uint64
		for _, nested := range field.Fields {
			if nested.Number == 1 {
				group = nested.Varint
			}
			if nested.Number == 2 {
				product = nested.Varint
			}
		}
		if group == 1100001 && product == 9100033 {
			duplicate.Fields = append(duplicate.Fields, field)
			break
		}
	}
	seed.Responses["/CashShopInfo"] = duplicate
	if _, err := seed.CashProductEventIndex(1100001, 9100033); err == nil {
		t.Fatal("duplicate cash product event was accepted")
	}
}
