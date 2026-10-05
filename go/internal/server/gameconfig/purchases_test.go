package gameconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiamondRechargeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		text, currency string
		invalid        bool
	}{
		{`{"schema_version":1}`, "free", false},
		{`{"schema_version":1,"purchases":{}}`, "free", false},
		{`{"schema_version":1,"purchases":{"diamond_recharge":{"currency":""}}}`, "", false},
		{`{"schema_version":1,"purchases":{"diamond_recharge":{"currency":"ban"}}}`, "ban", false},
		{`{"schema_version":1,"purchases":{"diamond_recharge":{"currency":"gold","gold_per_paid_diamond":10}}}`, "gold", false},
		{`{"schema_version":1,"purchases":{"diamond_recharge":{"currency":"diamonds","diamonds_per_paid_diamond":2}}}`, "diamonds", false},
		{`{"schema_version":1,"purchases":{"diamond_recharge":{"currency":"paid_diamonds"}}}`, "", true},
		{`{"schema_version":1,"purchases":{"diamond_recharge":{"currency":null}}}`, "", true},
		{`{"schema_version":1,"purchases":{"diamond_recharge":{"gold_per_paid_diamond":0}}}`, "", true},
		{`{"schema_version":1,"purchases":{"diamond_recharge":{"gold_per_paid_diamond":1000000001}}}`, "", true},
		{`{"schema_version":1,"purchases":{"diamond_recharge":{"diamonds_per_paid_diamond":1.1}}}`, "", true},
		{`{"schema_version":1,"purchases":{"diamond_recharge":{"typo":1}}}`, "", true},
	} {
		path := filepath.Join(t.TempDir(), FileName)
		if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if (err != nil) != tc.invalid {
			t.Fatalf("%s: error=%v", tc.text, err)
		}
		if err == nil && cfg.Purchases.DiamondRecharge.Currency != tc.currency {
			t.Fatalf("%s: %+v", tc.text, cfg.Purchases)
		}
	}
}
