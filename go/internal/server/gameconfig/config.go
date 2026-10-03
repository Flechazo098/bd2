// Package gameconfig loads server-authoritative gameplay rules.
package gameconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const FileName = "game.json"

type Config struct {
	SchemaVersion int         `json:"schema_version"`
	Gacha         GachaConfig `json:"gacha"`
}

type GachaConfig struct {
	IncludeCollaborationURWeapons bool `json:"include_collaboration_ur_weapons"`
}

func Default() Config { return Config{SchemaVersion: 1} }

// Load reads one explicit path. Older installations without the file retain
// the default rules; an existing invalid file must never silently fall back.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("game configuration: read %s: %w", path, err)
	}
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("game configuration: decode %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("game configuration: trailing data in %s", path)
	}
	// encoding/json accepts null for scalar and struct fields. These rules have
	// explicit defaults for omissions, while null is a configuration error.
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		return Config{}, fmt.Errorf("game configuration: decode %s: %w", path, err)
	}
	if containsNull(document) {
		return Config{}, fmt.Errorf("game configuration: null is not allowed in %s", path)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("game configuration: %s: %w", path, err)
	}
	return cfg, nil
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
		for _, child := range value {
			if containsNull(child) {
				return true
			}
		}
	}
	return false
}

func (c Config) Validate() error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema_version %d (expected 1)", c.SchemaVersion)
	}
	return nil
}

func BesideExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("game configuration: resolve server executable: %w", err)
	}
	return filepath.Join(filepath.Dir(executable), FileName), nil
}
