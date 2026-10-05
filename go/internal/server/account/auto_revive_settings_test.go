package account

import (
	"bd2server/internal/server/wire"
	"testing"
)

type autoSettingsFake struct {
	on    bool
	index uint64
}

func (p *autoSettingsFake) AutoReviveSettings() (bool, uint64, error) { return p.on, p.index, nil }
func TestLoginProjectsCurrentRecoverySettingsEachTime(t *testing.T) {
	s := &LoginSeed{Version: StateVersion(), PacketCode: 3, UserInfo: wire.AppendVarint(nil, 1, 42)}
	p := &autoSettingsFake{true, 199}
	if e := s.AttachAutoReviveSettings(p); e != nil {
		t.Fatal(e)
	}
	for _, on := range []bool{true, false} {
		p.on = on
		if !on {
			p.index = 0
		}
		b, e := s.Login(wire.AppendVarint(nil, 1, 1), []byte("0123456789abcdef0123456789abcdef"))
		if e != nil {
			t.Fatal(e)
		}
		user, _, _ := wire.Bytes(b, 1)
		v, _, _ := wire.Varint(user, 49)
		index, _, _ := wire.Varint(user, 50)
		if (v != 0) != on || index != p.index {
			t.Fatal("seed value leaked", v, index)
		}
	}
}
