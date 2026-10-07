//go:build windows

package accountstate

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/windows"
)

type writerLock struct {
	file       *os.File
	overlapped windows.Overlapped
	once       sync.Once
	err        error
}

func acquireWriterLock(path string) (*writerLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("accountstate: open writer lock: %w", err)
	}
	lock := &writerLock{file: file}
	err = windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&lock.overlapped,
	)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, fmt.Errorf("%w: %s", ErrWriterLocked, path)
		}
		return nil, fmt.Errorf("accountstate: acquire writer lock: %w", err)
	}
	return lock, nil
}

func (l *writerLock) release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		unlockErr := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped)
		closeErr := l.file.Close()
		if unlockErr != nil {
			unlockErr = fmt.Errorf("accountstate: release writer lock: %w", unlockErr)
		}
		if closeErr != nil {
			closeErr = fmt.Errorf("accountstate: close writer lock: %w", closeErr)
		}
		l.err = errors.Join(unlockErr, closeErr)
	})
	return l.err
}
