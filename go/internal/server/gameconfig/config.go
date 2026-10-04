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
	Story         StoryConfig `json:"story"`
}

// StartPackID selects the entry chapter for newly initialized accounts.
// Existing accounts retain their persisted entry and selected chapter.
type StoryConfig struct {
	StartPackID int `json:"start_pack_id"`
}

type GachaConfig struct {
	IncludeCollaborationURWeapons bool `json:"include_collaboration_ur_weapons"`
}

func Default() Config { return Config{SchemaVersion: 1, Story: StoryConfig{StartPackID: 21}} }

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
	cfg := Default()
	// The format version is required even when gameplay sections are omitted.
	cfg.SchemaVersion = 0
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
	if c.Story.StartPackID != 1 && c.Story.StartPackID != 21 {
		return fmt.Errorf("story.start_pack_id must be 1 or 21, got %d", c.Story.StartPackID)
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
