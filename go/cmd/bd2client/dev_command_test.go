//go:build !release

package main

import (
	"testing"
)

func TestDevelopmentPlanSkipsBackendRebuildAndPluginPreparation(t *testing.T) {
	args := []string{"--dev", "run", "--plan", "--game-dir", t.TempDir()}
	if relaunched, err := relaunchDevelopmentIfNeeded(args); relaunched || err != nil {
		t.Fatalf("inspection rebuilt the desktop backend: %v, %v", relaunched, err)
	}
	if handled, err := runClientDevelopmentCommand(args); !handled || err != nil {
		t.Fatalf("inspection attempted to prepare an incomplete game or start its UI: %v, %v", handled, err)
	}
}
