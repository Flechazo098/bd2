package account

import (
	"errors"
	"math"
	"testing"

	"bd2server/internal/server/wire"
)

type rewardStateSource struct {
	claimed, free, bonus uint64
	err                  error
}

func (p *rewardStateSource) LevelRewardCount() (uint64, error)  { return p.claimed, p.err }
func (p *rewardStateSource) HuntingAP() (uint64, uint64, error) { return p.free, p.bonus, p.err }

func TestLoginReadsMutableRewardAndHuntingState(t *testing.T) {
	user := wire.AppendVarint(nil, 1, 42)
	for _, field := range []int{13, 20, 21} {
		user = wire.AppendVarint(user, field, 999)
	}
	seed := &LoginSeed{Version: StateVersion(), PacketCode: 3, UserInfo: user}
	p := &rewardStateSource{claimed: 2, free: 40, bonus: 7}
	if err := seed.AttachLevelReward(p); err != nil {
		t.Fatal(err)
	}
	if err := seed.AttachHuntingAP(p); err != nil {
		t.Fatal(err)
	}
	for _, state := range []rewardStateSource{*p, {claimed: 3, free: 20, bonus: 0}, {}} {
		*p = state
		response, err := seed.Login(nil, []byte("0123456789abcdef0123456789abcdef"))
		if err != nil {
			t.Fatal(err)
		}
		actual, _, _ := wire.Bytes(response, 1)
		for field, want := range map[int]uint64{13: p.claimed, 20: p.free, 21: p.bonus} {
			got, _, err := wire.Varint(actual, field)
			if err != nil || got != want {
				t.Fatalf("field %d=%d want %d: %v", field, got, want, err)
			}
		}
	}
	for _, bad := range []rewardStateSource{{claimed: math.MaxInt32 + 1}, {free: math.MaxInt32 + 1}, {bonus: math.MaxInt32 + 1}, {err: errors.New("storage failed")}} {
		*p = bad
		if _, err := seed.Login(nil, []byte("0123456789abcdef0123456789abcdef")); err == nil {
			t.Fatal("invalid state accepted")
		}
	}
	if err := seed.AttachLevelReward(nil); err == nil {
		t.Fatal("nil reward provider accepted")
	}
	if err := seed.AttachHuntingAP(nil); err == nil {
		t.Fatal("nil AP provider accepted")
	}
}
