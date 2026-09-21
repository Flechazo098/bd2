package fixture

import (
	"fmt"
	"path/filepath"
	"testing"

	"bd2server/internal/wire"
)

func TestDebugBattleStartResponse(t *testing.T) {
	set, err := Load(filepath.Join("..", "..", "..", "data", "capture", "2.34.13", "20260920-003254"))
	if err != nil {
		t.Skip(err)
	}
	for _, record := range set.RecordedResponses("/BattleStart") {
		p, _, err := set.RecordedPayloadAt("/BattleStart", record.RequestSequence)
		if err != nil {
			t.Log(err)
			continue
		}
		_ = wire.Walk(p.Proto, func(f wire.Field) error {
			if f.Number == 1 || f.Number == 2 {
				vals := []string{}
				_ = wire.Walk(f.Value, func(x wire.Field) error {
					if x.Type == 0 {
						v, _, _ := wire.Varint(f.Value, x.Number)
						vals = append(vals, fmt.Sprintf("%d=%d", x.Number, v))
					}
					return nil
				})
				t.Logf("client=%d capture=%d top=%d raw=%x fields=%v", record.ClientSequence, record.RequestSequence, f.Number, f.Value, vals)
			}
			return nil
		})
	}
}
