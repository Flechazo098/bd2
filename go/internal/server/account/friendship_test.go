package account

import (
	"errors"
	"testing"

	"bd2server/internal/server/wire"
)

type friendshipAPFixture struct {
	remaining uint64
	err       error
}

func (f *friendshipAPFixture) FriendshipAP() (uint64, error) { return f.remaining, f.err }

func TestLoginReadsCurrentFriendshipAPInsteadOfSeed(t *testing.T) {
	seed := &LoginSeed{Version: StateVersion(), PacketCode: 3, UserInfo: wire.AppendVarint(nil, 1, 1)}
	seed.UserInfo = wire.AppendVarint(seed.UserInfo, 69, 3)
	seed.UserInfo = wire.AppendVarint(seed.UserInfo, 70, 99)
	provider := &friendshipAPFixture{remaining: 2}
	if err := seed.AttachFriendshipAP(provider); err != nil {
		t.Fatal(err)
	}
	for _, remaining := range []uint64{2, 0, 3} {
		provider.remaining = remaining
		body, err := seed.Login(wire.AppendVarint(nil, 1, 1), []byte("0123456789abcdef0123456789abcdef"))
		if err != nil {
			t.Fatal(err)
		}
		user, _, _ := wire.Bytes(body, 1)
		free, _, _ := wire.Varint(user, 69)
		stack, _, _ := wire.Varint(user, 70)
		if free != remaining || stack != 0 {
			t.Fatalf("friendship points free=%d stack=%d, want %d/0", free, stack, remaining)
		}
	}
	provider.err = errors.New("unavailable")
	if _, err := seed.Login(wire.AppendVarint(nil, 1, 1), []byte("0123456789abcdef0123456789abcdef")); err == nil {
		t.Fatal("login ignored friendship state error")
	}
}
