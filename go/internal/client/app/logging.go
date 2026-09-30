package app

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

const (
	clientLogName       = "bd2client.log"
	clientLogBackupName = "bd2client.log.1"
	clientLogMaxBytes   = 2 << 20
)

// OpenPersistentLogger creates the GUI client's bounded, persistent log next
// to the executable. The active log is capped at 2 MiB and one previous log is
// retained, so a client left installed for a long time cannot grow without
// limit.
func OpenPersistentLogger(executablePath string) (*slog.Logger, io.Closer, string, error) {
	if executablePath == "" {
		var err error
		executablePath, err = os.Executable()
		if err != nil {
			return nil, nil, "", fmt.Errorf("locate bd2client executable: %w", err)
		}
	}
	absolute, err := filepath.Abs(executablePath)
	if err != nil {
		return nil, nil, "", fmt.Errorf("resolve bd2client executable path: %w", err)
	}
	logDirectory := filepath.Join(filepath.Dir(absolute), "logs")
	if runtime.GOOS == "darwin" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return nil, nil, "", fmt.Errorf("locate macOS user home for logs: %w", homeErr)
		}
		logDirectory = filepath.Join(home, "Library", "Logs", "BD2 Client Studio")
	}
	if err := os.MkdirAll(logDirectory, 0o700); err != nil {
		return nil, nil, "", fmt.Errorf("create bd2client log directory: %w", err)
	}
	path := filepath.Join(logDirectory, clientLogName)
	writer, err := openRollingLog(path, filepath.Join(logDirectory, clientLogBackupName), clientLogMaxBytes)
	if err != nil {
		return nil, nil, "", err
	}
	logger := slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return logger, writer, path, nil
}

type rollingLog struct {
	mu         sync.Mutex
	path       string
	backupPath string
	maxBytes   int64
	file       *os.File
	size       int64
}

func openRollingLog(path, backupPath string, maxBytes int64) (*rollingLog, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open bd2client log: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect bd2client log: %w", err)
	}
	return &rollingLog{
		path:       path,
		backupPath: backupPath,
		maxBytes:   maxBytes,
		file:       file,
		size:       info.Size(),
	}, nil
}

func (w *rollingLog) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, os.ErrClosed
	}
	if w.size > 0 && w.size+int64(len(data)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	written, err := w.file.Write(data)
	w.size += int64(written)
	return written, err
}

func (w *rollingLog) rotate() error {
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("close bd2client log for rotation: %w", err)
	}
	w.file = nil
	if err := os.Remove(w.backupPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replace bd2client log backup: %w", err)
	}
	if err := os.Rename(w.path, w.backupPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rotate bd2client log: %w", err)
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create rotated bd2client log: %w", err)
	}
	w.file = file
	w.size = 0
	return nil
}

func (w *rollingLog) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
