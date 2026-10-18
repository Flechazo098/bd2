// Package config provides JSON configuration I/O with explicit decoding policies.
package config

import (
	"bd2server/internal/common/util"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
)

type DecodeOptions struct {
	DisallowUnknownFields bool
	RejectTrailing        bool
	AllowBOM              bool
	RejectNull            bool
}

type WriteOptions struct {
	FileMode      os.FileMode
	DirectoryMode os.FileMode
}

// Decode overlays one JSON value onto initial, preserving defaults for omissions.
func Decode[T any](raw []byte, initial T, options DecodeOptions) (T, error) {
	var zero T
	if options.AllowBOM {
		raw = bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if options.DisallowUnknownFields {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(&initial); err != nil {
		return zero, err
	}
	if options.RejectTrailing {
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return zero, errors.New("configuration must contain exactly one JSON value")
		}
	}
	if options.RejectNull {
		var document any
		if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&document); err != nil {
			return zero, err
		}
		if containsNull(document) {
			return zero, errors.New("configuration must not contain null")
		}
	}
	return initial, nil
}

func Load[T any](path string, initial T, options DecodeOptions) (T, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		var zero T
		return zero, err
	}
	return Decode(raw, initial, options)
}

func Save[T any](path string, value T, options WriteOptions) error {
	temporary, err := stage(path, value, options)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary) }()
	return util.ReplaceFile(temporary, path)
}

// Ensure publishes defaults atomically and never replaces an existing file.
func Ensure[T any](path string, defaults T, options WriteOptions) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := stage(path, defaults, options)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary) }()
	if err := os.Link(temporary, path); errors.Is(err, os.ErrExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("create default configuration %s: %w", path, err)
	}
	slog.Info("default configuration created", "path", path)
	return nil
}

func stage[T any](path string, value T, options WriteOptions) (string, error) {
	raw, err := encode(value)
	if err != nil {
		return "", err
	}
	return stageBytes(path, raw, options)
}

// WriteFile atomically replaces path with raw. The file is created with mode
// 0600 and its directory with 0755 when missing.
func WriteFile(path string, raw []byte) error {
	temporary, err := stageBytes(path, raw, WriteOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary) }()
	return util.ReplaceFile(temporary, path)
}

func stageBytes(path string, raw []byte, options WriteOptions) (string, error) {
	if options.FileMode == 0 {
		options.FileMode = 0o600
	}
	if options.DirectoryMode == 0 {
		options.DirectoryMode = 0o755
	}
	if err := os.MkdirAll(filepath.Dir(path), options.DirectoryMode); err != nil {
		return "", fmt.Errorf("create configuration directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return "", err
	}
	if err = file.Chmod(options.FileMode); err == nil {
		_, err = file.Write(raw)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}

func containsNull(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case map[string]any:
		for _, child := range value {
			if containsNull(child) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(value, containsNull)
	}
	return false
}
