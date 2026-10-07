//go:build linux || darwin

package accountstate

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

type writerLock struct {
	file *os.File
	once sync.Once
	err  error
}

func acquireWriterLock(path string) (*writerLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("accountstate: open writer lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", ErrWriterLocked, path)
		}
		return nil, fmt.Errorf("accountstate: acquire writer lock: %w", err)
	}
	return &writerLock{file: file}, nil
}

func (l *writerLock) release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
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
