package account

import (
	"testing"

	"bd2server/internal/server/wire"
)

type mutableFirstGachaStatus struct{ completed bool }

func (p *mutableFirstGachaStatus) FirstGachaCompleted() bool { return p.completed }

func TestLoginReadsFirstGachaCompletionAtEachLogin(t *testing.T) {
	// An old immutable seed value must be replaced by authoritative account
	// state, then immediately change on the next login after confirmation.
	seed := &LoginSeed{Version: StateVersion(), PacketCode: 11, UserInfo: wire.AppendVarint(wire.AppendVarint(nil, 1, 42), 27, 1)}
	status := &mutableFirstGachaStatus{}
	if err := seed.AttachFirstGacha(status); err != nil {
		t.Fatal(err)
	}
	for _, completed := range []bool{false, true, false} {
		status.completed = completed
		body, err := seed.Login(wire.AppendVarint(nil, 1, 4), []byte("0123456789abcdef0123456789abcdef"))
		if err != nil {
			t.Fatal(err)
		}
		user, found, err := wire.Bytes(body, 1)
		if err != nil || !found {
			t.Fatalf("user found=%v err=%v", found, err)
		}
		value, _, err := wire.Varint(user, 27)
		if err != nil || (value == 1) != completed {
			t.Fatalf("first gacha=%d want completed=%v err=%v", value, completed, err)
		}
	}
}

func TestLoginRejectsMissingFirstGachaProvider(t *testing.T) {
	if err := (&LoginSeed{}).AttachFirstGacha(nil); err == nil {
		t.Fatal("missing provider accepted")
	}
}
