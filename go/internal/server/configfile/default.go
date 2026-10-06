// Package configfile publishes default configuration without replacing user files.
package configfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// Ensure publishes a complete JSON file only when path does not exist.
// Linking a same-directory temporary file makes publication atomic and refuses
// to replace a configuration created by another process in the meantime.
func Ensure(path string, defaults any) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.MarshalIndent(defaults, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Link(file.Name(), path); errors.Is(err, os.ErrExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("create default configuration %s: %w", path, err)
	}
	slog.Info("default configuration created", "path", path)
	return nil
}
