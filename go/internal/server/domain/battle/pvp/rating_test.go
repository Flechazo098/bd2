package pvp

import (
	"testing"

	"bd2server/internal/server/protocol/wire"
)

func battleEnd(result uint64, stats ...uint64) []byte {
	var report []byte
	for i, v := range stats {
		report = wire.AppendVarint(report, i+1, v)
	}
	return wire.AppendBytes(wire.AppendVarint(nil, 2, result), 100, report)
}

// FBattle reports remaining health as field 100 of the end request; the
// margin is the attacker's remaining share minus the defender's.
func TestBattleStatsMarginFromPluginReport(t *testing.T) {
	stats, err := parseBattleStats(battleEnd(1, 300, 1200, 0, 1000, 2, 0, 7), 1)
	if err != nil {
		t.Fatal(err)
	}
	if m := stats.Margin(); m == nil || *m != 0.25 {
		t.Fatalf("margin %v, want 0.25", m)
	}
	if _, err := parseBattleStats(battleEnd(1, 0, 1200, 500, 1000, 0, 3, 7), 1); err == nil {
		t.Fatal("a win with the attacker wiped out and defenders standing was accepted")
	}
	if stats, err := parseBattleStats(wire.AppendVarint(nil, 2, 1), 1); err != nil || stats.Margin() != nil {
		t.Fatalf("official client without a report: %+v, %v", stats, err)
	}
}
