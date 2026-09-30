//go:build release

package main

import "testing"

func TestReleaseBuildDoesNotHandleDevelopmentCommand(t *testing.T) {
	handled, err := runDevelopmentCommand([]string{"--dev", "run"})
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("release build unexpectedly handled --dev run")
	}
}
