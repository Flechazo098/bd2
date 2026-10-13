package inventory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDevelopmentSettingsRejectMalformedAccountConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev-tools.json")
	for _, document := range []string{
		`{"version":1,"inventory":{"unlimited":true}}`,
		`{"version":2}`,
		`{"version":2,"accounts":null}`,
		`{"version":2,"accounts":{"A":null}}`,
		`{"version":2,"accounts":{"A":{"inventory":null}}}`,
		`{"version":2,"accounts":{"A":{"inventory":{}}}}`,
		`{"version":2,"accounts":{"A":{"inventory":{"unlimited":null}}}}`,
		`{"version":2,"accounts":{"A":{"inventory":{"unlimited":"true"}}}}`,
		`{"version":2,"accounts":{"A":{"inventory":{"unlimited":true,"other":false}}}}`,
		`{"version":2,"accounts":{"A":{"inventory":{"unlimited":true}}}} {}`,
	} {
		if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadUnlimitedInventory(path, "B"); err == nil {
			t.Fatalf("malformed settings accepted for unrelated account: %s", document)
		}
	}
}

func TestMissingDevelopmentSettingsDisableOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev-tools.json")
	if unlimited, err := loadUnlimitedInventory(path, "A"); err != nil || unlimited {
		t.Fatalf("missing settings: unlimited=%v error=%v", unlimited, err)
	}
	if err := os.WriteFile(path, []byte(`{"version":2,"accounts":{"A":{"inventory":{"unlimited":true}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, account := range []string{"B", ""} {
		if unlimited, err := loadUnlimitedInventory(path, account); err != nil || unlimited {
			t.Fatalf("missing account %q: unlimited=%v error=%v", account, unlimited, err)
		}
	}
}
