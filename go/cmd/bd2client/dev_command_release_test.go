//go:build release

package main

import "testing"

func TestReleaseBuildRejectsDevelopmentFlag(t *testing.T) {
	args, options, err := developmentRunOptions([]string{"--dev", "run"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runClient(args, options); err == nil {
		t.Fatal("release build unexpectedly accepted --dev run")
	}
}
