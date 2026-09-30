package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	return filepath.Join(root, "BD2 Client Studio", "preferences.json"), nil
}

func LoadPreferences() (Preferences, error) {
	path, err := PreferencesPath()
	if err != nil {
		return Preferences{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Preferences{}, nil
	}
	if err != nil {
		return Preferences{}, fmt.Errorf("read client preferences: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var preferences Preferences
	if err := decoder.Decode(&preferences); err != nil {
		return Preferences{}, fmt.Errorf("decode client preferences: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Preferences{}, errors.New("client preferences must contain exactly one JSON object")
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create client preferences directory: %w", err)
	}
	data, err := json.MarshalIndent(Preferences{SchemaVersion: preferencesSchemaVersion, GameDirectory: abs}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".preferences-*.tmp")
	if err != nil {
		return fmt.Errorf("stage client preferences: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("stage client preferences: %w", err)
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return fmt.Errorf("install client preferences: %w", err)
	}
	return nil
}
