package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	commonconfig "bd2server/internal/common/config"
)

const preferencesSchemaVersion = 1

type Preferences struct {
	SchemaVersion int    `json:"schema_version"`
	GameDirectory string `json:"game_directory"`
}

func PreferencesPath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user configuration directory: %w", err)
	}
	return filepath.Join(root, "BD2 Launcher", "preferences.json"), nil
}

func LoadPreferences() (Preferences, error) {
	path, err := PreferencesPath()
	if err != nil {
		return Preferences{}, err
	}
	preferences, err := commonconfig.Load(path, Preferences{}, commonconfig.DecodeOptions{DisallowUnknownFields: true, RejectTrailing: true})
	if errors.Is(err, os.ErrNotExist) {
		return Preferences{}, nil
	}
	if err != nil {
		return Preferences{}, fmt.Errorf("load client preferences: %w", err)
	}
	if preferences.SchemaVersion != preferencesSchemaVersion || strings.TrimSpace(preferences.GameDirectory) == "" {
		return Preferences{}, errors.New("client preferences are invalid")
	}
	preferences.GameDirectory = filepath.Clean(preferences.GameDirectory)
	return preferences, nil
}

func SavePreferences(gameDirectory string) error {
	abs, err := filepath.Abs(filepath.Clean(strings.TrimSpace(gameDirectory)))
	if err != nil || strings.TrimSpace(gameDirectory) == "" {
		return errors.New("client preferences require a valid game directory")
	}
	path, err := PreferencesPath()
	if err != nil {
		return err
	}
	preferences := Preferences{SchemaVersion: preferencesSchemaVersion, GameDirectory: abs}
	if current, err := LoadPreferences(); err == nil && current == preferences {
		return nil
	}
	if err := commonconfig.Save(path, preferences, commonconfig.WriteOptions{DirectoryMode: 0o700}); err != nil {
		return fmt.Errorf("save client preferences: %w", err)
	}
	return nil
}
