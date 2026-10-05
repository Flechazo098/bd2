//go:build !release

package main

import (
	"os"
	"testing"
)

// Opt in on a developer machine with the game installed. Exercise the exact
// --dev run preparation path without starting the interactive Wails window.
func TestDevelopmentRunBuildsPluginsWithEmbeddedNames(t *testing.T) {
	if os.Getenv("BD2_TEST_DEVELOPMENT_BUILD") != "1" {
		t.Skip("set BD2_TEST_DEVELOPMENT_BUILD=1 to build against go/config.json")
	}
	args, options, err := developmentRunOptions([]string{"--dev", "run"})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 0 {
		t.Fatalf("unexpected runtime arguments: %v", args)
	}
	for _, path := range []string{options.localIdentityPlugin, options.loginUIPlugin, options.cashShopPlugin} {
		if !clientDevelopmentFile(path) {
			t.Fatalf("development plugin output missing: %s", path)
		}
	}
}
