package account

import (
	"errors"
	"math"
	"testing"

	"bd2server/internal/server/wire"
)

type testAchievementExperience struct {
	value uint64
	err   error
}

func (s *testAchievementExperience) AchievementExperience() (uint64, error) { return s.value, s.err }

func TestLoginReadsPersistedAchievementExperienceEachTime(t *testing.T) {
	seed := &LoginSeed{Version: StateVersion(), PacketCode: 3, UserInfo: wire.AppendVarint(wire.AppendVarint(nil, 1, 42), 12, 999)}
	source := &testAchievementExperience{value: 7}
	if err := seed.AttachAchievementExperience(source); err != nil {
		t.Fatal(err)
	}
	for _, value := range []uint64{7, 16, 0} {
		source.value = value
		response, err := seed.Login(wire.AppendVarint(nil, 1, 1), []byte("0123456789abcdef0123456789abcdef"))
		if err != nil {
			t.Fatal(err)
		}
		user, _, _ := wire.Bytes(response, 1)
		exp, found, err := wire.Varint(user, 12)
		if err != nil || !found || exp != value {
			t.Fatalf("exp=%d want=%d err=%v", exp, value, err)
		}
	}
	for _, invalid := range []testAchievementExperience{{value: math.MaxInt32 + 1}, {err: errors.New("read failed")}} {
		*source = invalid
		if _, err := seed.Login(wire.AppendVarint(nil, 1, 1), []byte("0123456789abcdef0123456789abcdef")); err == nil {
			t.Fatal("invalid experience accepted")
		}
	}
}
