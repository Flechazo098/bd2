//go:build !windows

package download

func requireStopped(string) error { return nil }
