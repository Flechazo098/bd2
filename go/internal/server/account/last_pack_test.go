package account

import (
	"errors"
	"testing"

	"bd2server/internal/server/wire"
)

type lastPackFixture struct {
	pack uint64
	err  error
}

func (f *lastPackFixture) LastPlayedPackID() (uint64, error) { return f.pack, f.err }

func TestLoginUsesSavedPackAndPreservesSeedOnlyForNewAccount(t *testing.T) {
	seed := &LoginSeed{Version: StateVersion(), PacketCode: 3, UserInfo: wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 4, 21)}
	provider := &lastPackFixture{}
	if err := seed.AttachLastPlayedPack(provider); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ saved, want uint64 }{{0, 21}, {3001, 3001}, {22, 22}} {
		provider.pack = tc.saved
		body, err := seed.Login(wire.AppendVarint(nil, 1, 1), []byte("0123456789abcdef0123456789abcdef"))
		if err != nil {
			t.Fatal(err)
		}
		user, _, _ := wire.Bytes(body, 1)
		pack, _, _ := wire.Varint(user, 4)
		if pack != tc.want {
			t.Fatalf("saved=%d login pack=%d want=%d", tc.saved, pack, tc.want)
		}
	}
	provider.err = errors.New("position unavailable")
	if _, err := seed.Login(nil, []byte("0123456789abcdef0123456789abcdef")); err == nil {
		t.Fatal("ignored saved position error")
	}
}
