//go:build linux

package statetx

import (
	"os"

	"golang.org/x/sys/unix"
)

// Linux first asks the filesystem for an FICLONE copy-on-write snapshot
// (btrfs, XFS and supporting filesystems). Byte copying remains the safe
// fallback; hard links are deliberately avoided because in-place writes by a
// future Store implementation would also mutate the undo image.
func snapshotBefore(source, destination string) error {
	if err := reflinkLinux(source, destination); err == nil {
		return nil
	}
	_ = os.Remove(destination)
	return copySynced(source, destination)
}

func reflinkLinux(source, destination string) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err = unix.IoctlFileClone(int(dst.Fd()), int(src.Fd())); err == nil {
		err = dst.Sync()
	}
	if closeErr := dst.Close(); err == nil {
		err = closeErr
	}
	return err
}
