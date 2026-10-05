package gamedata

import (
	"os"
	"testing"
)

// Opt-in performance verification, not a source for game design assertions.
func BenchmarkInstalledWaypointPackWarm(b *testing.B) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		b.Skip("BD2_REAL_GAMEDATA not configured")
	}
	if err := CloseDatabaseCache(); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := CloseDatabaseCache(); err != nil {
			b.Error(err)
		}
	})
	if _, err := LoadWaypointPack(root, "20260923193640", 21); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := LoadWaypointPack(root, "20260923193640", 21); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInstalledWaypointPackCold(b *testing.B) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		b.Skip("BD2_REAL_GAMEDATA not configured")
	}
	b.Cleanup(func() { _ = CloseDatabaseCache() })
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		if err := CloseDatabaseCache(); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if _, err := LoadWaypointPack(root, "20260923193640", 21); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInstalledPackDetailWarm(b *testing.B) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		b.Skip("BD2_REAL_GAMEDATA not configured")
	}
	if err := CloseDatabaseCache(); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = CloseDatabaseCache() })
	if _, err := LoadPackDetailDesign(root, "20260923193640", 21); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := LoadPackDetailDesign(root, "20260923193640", 21); err != nil {
			b.Fatal(err)
		}
	}
}
