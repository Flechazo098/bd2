package life_test

import (
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
)

// LifeItemTable 501 stacks to 1000 in this catalog. The account override lifts
// that limit; once it is off, the excess is cut back on the next trim.
func TestUnlimitedStacksLiftLifeItemLimitUntilTrimmed(t *testing.T) {
	g := scenario(t)
	unlimited := true
	g.options.UnlimitedStacks = func(command.Context) (bool, error) { return unlimited, nil }
	g.reopen()
	wood := func() uint64 {
		t.Helper()
		count, err := g.s.Balance(g.ctx, 66, 501)
		if err != nil {
			t.Fatal(err)
		}
		return count
	}
	g.fund("unlimited", 0, 1500)
	if got := wood(); got != 1500 {
		t.Fatalf("unlimited wood = %d, want 1500", got)
	}
	if trimmed, err := g.s.TrimItemStacks(g.ctx); err != nil || trimmed || wood() != 1500 {
		t.Fatalf("trim while enabled: wood=%d trimmed=%v err=%v", wood(), trimmed, err)
	}
	unlimited = false
	if _, err := g.s.Apply(g.ctx, "limited", nil, []gamedata.Reward{{Type: 66, ID: 501, Count: 1}}); err == nil {
		t.Fatal("limited grant accepted beyond the stack limit")
	}
	if trimmed, err := g.s.TrimItemStacks(g.ctx); err != nil || !trimmed {
		t.Fatalf("trim after disabling: trimmed=%v err=%v", trimmed, err)
	}
	if got := wood(); got != 1000 {
		t.Fatalf("trimmed wood = %d, want 1000", got)
	}
	g.reopen()
	if got := wood(); got != 1000 {
		t.Fatalf("trim did not persist: %d", got)
	}
}
