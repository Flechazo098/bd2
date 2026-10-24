package inventory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDevelopmentSettingsRejectMalformedAccountConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev-tools.json")
	for _, document := range []string{
		`{"version":2,"accounts":{"A":{"inventory":{"unlimited":true}}}}`,
		`{"version":3}`,
		`{"version":3,"accounts":null}`,
		`{"version":3,"accounts":{"A":null}}`,
		`{"version":3,"accounts":{"A":{"items":null}}}`,
		`{"version":3,"accounts":{"A":{"items":{}}}}`,
		`{"version":3,"accounts":{"A":{"items":{"unlimited_stacks":null}}}}`,
		`{"version":3,"accounts":{"A":{"items":{"unlimited_stacks":"true"}}}}`,
		`{"version":3,"accounts":{"A":{"items":{"unlimited_stacks":true,"other":false}}}}`,
		`{"version":3,"accounts":{"A":{"items":{"unlimited_stacks":true}}}} {}`,
	} {
		if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := UnlimitedItemStacks(path, "B"); err == nil {
			t.Fatalf("malformed settings accepted for unrelated account: %s", document)
		}
	}
}

func TestMissingDevelopmentSettingsDisableOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev-tools.json")
	if unlimited, err := UnlimitedItemStacks(path, "A"); err != nil || unlimited {
		t.Fatalf("missing settings: unlimited=%v error=%v", unlimited, err)
	}
	if err := os.WriteFile(path, []byte(`{"version":3,"accounts":{"A":{"items":{"unlimited_stacks":true}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if unlimited, err := UnlimitedItemStacks(path, "A"); err != nil || !unlimited {
		t.Fatalf("enabled account: unlimited=%v error=%v", unlimited, err)
	}
	for _, account := range []string{"B", ""} {
		if unlimited, err := UnlimitedItemStacks(path, account); err != nil || unlimited {
			t.Fatalf("missing account %q: unlimited=%v error=%v", account, unlimited, err)
		}
	}
}
