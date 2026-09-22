//go:build darwin

package statetx

import (
	"os"

	"golang.org/x/sys/unix"
)

// APFS clonefile creates an independent copy-on-write snapshot. HFS+ and
// other filesystems safely fall back to a synchronized byte copy.
func snapshotBefore(source, destination string) error {
	if err := unix.Clonefile(source, destination, 0); err == nil {
		file, openErr := os.OpenFile(destination, os.O_WRONLY, 0)
		if openErr == nil {
			openErr = file.Sync()
			if closeErr := file.Close(); openErr == nil {
				openErr = closeErr
			}
		}
		if openErr == nil {
			return nil
		}
	}
	_ = os.Remove(destination)
	return copySynced(source, destination)
}
