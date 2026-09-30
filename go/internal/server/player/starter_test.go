package player

import (
	"bytes"
	"path/filepath"
	"testing"

	"bd2server/internal/server/wire"
)

func TestStarterCanAnswerWithoutCapture(t *testing.T) {
	seed, err := Load(filepath.Join("..", "..", "..", "seed", "v2_35_10", "starter_player.json"))
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

func TestStarterProtocolEncodingAndRoundTrip(t *testing.T) {
	seed := &Starter{
		Version:                  "2.35.10",
		Items:                    []Item{{InvenIndex: 1, ID: 2, Type: 8, Count: 3}},
		Costumes:                 []Costume{{InvenIndex: 4, ID: 5, UseChar: 6}},
		Characters:               []Character{{InvenIndex: 6, ID: 7, HP: 8, Level: 1, UseCostume: 4, ConnectPotentialCostume: 5}},
		FieldCharControlDeckType: 2,
	}
	path := filepath.Join(t.TempDir(), "starter.json")
	if err := seed.Write(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path  string
		code  int
		proto []byte
	}{
		{"/ItemInfo", 21, []byte{0x0a, 8, 0x08, 1, 0x10, 2, 0x18, 8, 0x20, 3}},
		{"/CostumeInfo", 40, []byte{0x0a, 6, 0x08, 4, 0x10, 5, 0x20, 6}},
		{"/CharInfo", 9, []byte{0x0a, 12, 0x08, 6, 0x10, 7, 0x18, 8, 0x20, 1, 0x38, 4, 0x68, 5, 0x10, 2}},
	} {
		t.Run(test.path, func(t *testing.T) {
			code, proto, handled, err := loaded.Handle(test.path, wire.AppendVarint(nil, 1, 900001))
			if err != nil || !handled || code != test.code || !bytes.Equal(proto, test.proto) {
				t.Fatalf("code=%d proto=%x handled=%v err=%v", code, proto, handled, err)
			}
			for _, request := range [][]byte{nil, {0x08, 0}, {0x08, 0x80}} {
				if _, _, handled, err := loaded.Handle(test.path, request); !handled || err == nil {
					t.Fatal("invalid request accepted")
				}
			}
		})
	}
}
