package player

import (
	"bytes"
	"path/filepath"
	"testing"

	"bd2server/internal/fixture"
	"bd2server/internal/wire"
)

func TestStarterCanAnswerWithoutCapture(t *testing.T) {
	seed, err := Load(filepath.Join("..", "..", "seed", "v2_34_13", "starter_player.json"))
	if err != nil {
		t.Fatal(err)
	}
	for path, code := range map[string]int{"/ItemInfo": 21, "/CostumeInfo": 40, "/CharInfo": 9} {
		actual, proto, ok, err := seed.Handle(path, wire.AppendVarint(nil, 1, 900001))
		if err != nil || !ok || actual != code || len(proto) == 0 {
			t.Fatalf("%s response: code=%d len=%d ok=%v err=%v", path, actual, len(proto), ok, err)
		}
	}
	if _, _, ok, err := seed.Handle("/NotImplemented", nil); ok || err != nil {
		t.Fatal("accepted unknown endpoint")
	}
}

func TestStarterMatchesInitialAccountSample(t *testing.T) {
	root := filepath.Join("..", "..", "..", "data", "capture", "2.34.13", "20260920-003254")
	set, err := fixture.Load(root)
	if err != nil {
		t.Skipf("optional capture unavailable: %v", err)
	}
	key, err := set.SessionKey()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := set.BatchResponse(1)
	if err != nil {
		t.Fatal(err)
	}
	_, recorded, err := fixture.DecodeBatch(batch, []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	seed, err := Load(filepath.Join("..", "..", "seed", "v2_34_13", "starter_player.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/ItemInfo", "/CostumeInfo", "/CharInfo"} {
		_, proto, ok, err := seed.Handle(path, wire.AppendVarint(nil, 1, 900001))
		if err != nil || !ok || !bytes.Equal(proto, recorded[path]) {
			t.Errorf("%s parsed player starter differs from source: got=%x want=%x err=%v", path, proto, recorded[path], err)
		}
	}
}
