package feature

import (
	"bytes"
	"testing"

	"bd2server/internal/server/wire"
)

func TestBootstrapProtocolDefaults(t *testing.T) {
	for _, test := range []struct {
		path  string
		code  int
		proto []byte
	}{
		{"/CashMailInfo", 140, []byte{0x18, 0}},
		{"/AvatarInfo", 467, []byte{0x0a, 0}},
		{"/GuildRaidSeasonReward", 310, []byte{0x0a, 0}},
	} {
		t.Run(test.path, func(t *testing.T) {
			code, proto, handled, err := Handle(test.path, wire.AppendVarint(nil, 1, 42))
			if err != nil || !handled || code != test.code || !bytes.Equal(proto, test.proto) {
				t.Fatalf("code=%d proto=%x handled=%v err=%v", code, proto, handled, err)
			}
			if len(proto) != 0 {
				proto[0] ^= 0xff
			}
			_, again, _, err := Handle(test.path, wire.AppendVarint(nil, 1, 43))
			if err != nil || !bytes.Equal(again, test.proto) {
				t.Fatalf("response mutation escaped into defaults: proto=%x err=%v", again, err)
			}
			if _, _, handled, err := Handle(test.path, nil); !handled || err == nil {
				t.Fatal("bootstrap endpoint accepted missing sequence")
			}
		})
	}
}
