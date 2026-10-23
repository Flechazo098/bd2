//go:build !windows

package util

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// TryLockFile takes an exclusive process lock, released when the file closes.
func TryLockFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return file, nil
}
