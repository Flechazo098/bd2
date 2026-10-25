package castle

import (
	"fmt"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
)

// 2.37.13 EvilCastleInfo maps gates 2/3/4 to modes 16/19/20;
// EvilCastlePacket.GetClearCountEvilCastleBTower counts floors from one.
func TestClearPackageTowerProgressUsesContiguousFloorsOfItsOwnTower(t *testing.T) {
	for _, tc := range []struct{ gate, mode, other uint64 }{{2, 16, 19}, {3, 19, 20}, {4, 20, 16}} {
		t.Run(fmt.Sprint(tc.gate), func(t *testing.T) {
			d := testDesign()
			d.Towers = map[[2]uint64]gamedata.CastleTower{{tc.mode, 1}: {Mode: tc.mode, ID: 1}, {tc.mode, 2}: {Mode: tc.mode, ID: 2}}
			s := NewService(d)
			ctx := command.Context{State: stateio.NewMemory()}
			v := &state{Towers: map[string]tower{towerKey(tc.mode, 2): {ID: 2, Mode: tc.mode}, towerKey(tc.other, 1): {ID: 1, Mode: tc.other}}, Floors: map[uint64]floor{}}
			check := func(want bool) {
				t.Helper()
				got, err := s.CashPackageTowerCleared(ctx, tc.gate, 2)
				if err != nil || got != want {
					t.Fatalf("cleared=%v err=%v, want %v", got, err, want)
				}
			}
			if err := save(ctx, v); err != nil {
				t.Fatal(err)
			}
			check(false)
			v.Towers[towerKey(tc.mode, 1)] = tower{ID: 1, Mode: tc.mode}
			if err := save(ctx, v); err != nil {
				t.Fatal(err)
			}
			s = NewService(d)
			check(true)
			other := command.Context{State: stateio.NewMemory()}
			if got, err := s.CashPackageTowerCleared(other, tc.gate, 2); err != nil || got {
				t.Fatalf("another account inherited progress: %v %v", got, err)
			}
			if err := ctx.State.PutEntry("missions", "castle_state", "progress", []byte("invalid")); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CashPackageTowerCleared(ctx, tc.gate, 2); err == nil {
				t.Fatal("corrupt progress was treated as a normal eligibility result")
			}
		})
	}
}
