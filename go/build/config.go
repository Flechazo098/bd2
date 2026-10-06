package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func readJSON(path string, v any, strict bool) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})))
	if strict {
		d.DisallowUnknownFields()
	}
	if err = d.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("%s must contain one JSON object", path)
	}
	return nil
}
func (t task) gameDirectory(explicit string) (string, string, string, error) {
	if explicit == "" {
		var c struct {
			SchemaVersion int    `json:"schema_version"`
			GameDirectory string `json:"game_directory"`
		}
		if err := readJSON(filepath.Join(t.root, "go", "config.json"), &c, true); err != nil {
			return "", "", "", fmt.Errorf("read go/config.json or pass -GameDir: %w", err)
		}
		if c.SchemaVersion != 1 || strings.TrimSpace(c.GameDirectory) == "" {
			return "", "", "", errors.New("go/config.json requires schema_version 1 and game_directory")
		}
		explicit = c.GameDirectory
	}
	game, err := filepath.Abs(explicit)
	if err != nil {
		return "", "", "", err
	}
	managed := filepath.Join(game, "BrownDust II_Data", "Managed")
	bep := filepath.Join(game, "BepInEx")
	if !regular(filepath.Join(managed, "Assembly-CSharp.dll")) {
		app := game
		if !strings.EqualFold(filepath.Ext(app), ".app") {
			app = filepath.Join(game, "BrownDust II.app")
		}
		managed = filepath.Join(app, "Contents", "Resources", "Data", "Managed")
		bep = filepath.Join(filepath.Dir(app), "BepInEx")
		inside := filepath.Join(app, "Contents", "BepInEx")
		if directory(inside) && !directory(bep) {
			bep = inside
		}
	}
	if !regular(filepath.Join(managed, "Assembly-CSharp.dll")) {
		return "", "", "", fmt.Errorf("Assembly-CSharp.dll missing in %s", game)
	}
	if !regular(filepath.Join(bep, "core", "BepInEx.dll")) {
		return "", "", "", fmt.Errorf("BepInEx/core/BepInEx.dll missing in %s", game)
	}
	return game, managed, bep, nil
}
