//go:build !windows

package util

import "os"

// ReplaceFile atomically renames source over destination, replacing it if present.
func ReplaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
