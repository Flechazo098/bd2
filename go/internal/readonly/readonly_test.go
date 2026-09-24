package readonly

import (
	"bytes"
	"path/filepath"
	"testing"

	"bd2server/internal/fixture"
	"bd2server/internal/wire"
)

func TestSeedByteCompatibility(t *testing.T) {
	root := filepath.Join("..", "..", "..", "data", "capture", "2.34.13", "20260920-003254")
	set, err := fixture.Load(root)
	if err != nil {
		t.Skipf("optional capture unavailable: %v", err)
	}
	key, err := set.SessionKey()
	if err != nil {
		t.Fatal(err)
	}
	seed, err := Load(filepath.Join("..", "..", "seed", "v2_34_13", "readonly.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		PacketCode int
		Proto      []byte
	}{}
	for batch := 1; batch <= 2; batch++ {
		body, err := set.BatchResponse(batch)
		if err != nil {
			t.Fatal(err)
		}
		items, decoded, err := fixture.DecodeBatch(body, []byte(key))
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if _, ok := seed.Responses[item.Path]; ok {
				want[item.Path] = struct {
					PacketCode int
					Proto      []byte
				}{item.Response.PacketCode, decoded[item.Path]}
			}
		}
	}
	if len(want) != len(seed.Responses) {
		t.Fatalf("capture has %d seed responses, want %d", len(want), len(seed.Responses))
	}
	for path, expected := range want {
		code, got, ok, err := seed.Handle(path, wire.AppendVarint(nil, 1, 999))
		if err != nil || !ok || code != expected.PacketCode || !bytes.Equal(got, expected.Proto) {
			t.Errorf("%s: code=%d want=%d equal=%v err=%v", path, code, expected.PacketCode, bytes.Equal(got, expected.Proto), err)
		}
	}
}

func TestCashProductEventIndexIsUniqueSemanticSeedFact(t *testing.T) {
	seed, err := Load(filepath.Join("..", "..", "seed", "v2_34_13", "readonly.json"))
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
