//go:build !release

package main

import "testing"

func TestAppendDefaultFlagPreservesExplicitOverride(t *testing.T) {
	for _, args := range [][]string{{"--data-dir", "custom"}, {"--data-dir=custom"}} {
		got := appendDefaultFlag(append([]string(nil), args...), "--data-dir", "default")
		if len(got) != len(args) {
			t.Fatalf("args=%v got=%v", args, got)
		}
	}
	got := appendDefaultFlag(nil, "--data-dir", "default")
	if len(got) != 2 || got[0] != "--data-dir" || got[1] != "default" {
		t.Fatalf("default args=%v", got)
	}
}
