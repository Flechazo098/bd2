package gamedata

import "testing"

func TestPackRecoveryUsesPackTypesAndCompletion(t *testing.T) {
	p := &PackRecoveryPolicy{Types: map[int]uint64{99: 0, 101: 1, 303: 4, 909: 6, 707: 1000}}
	for _, v := range []struct {
		pack           int
		complete, want bool
	}{{99, false, true}, {101, false, false}, {101, true, true}, {303, true, false}, {909, false, false}, {909, true, true}, {707, false, true}, {123, true, false}} {
		if got := p.Allowed(v.pack, v.complete); got != v.want {
			t.Fatalf("pack%d complete%v got%v", v.pack, v.complete, got)
		}
	}
}
