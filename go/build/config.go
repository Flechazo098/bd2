package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	commonconfig "bd2server/internal/common/config"
)

func (t task) gameDirectory(explicit string) (string, string, string, error) {
	if explicit == "" {
		c, err := commonconfig.Load(filepath.Join(t.root, "go", "config.json"), struct {
			SchemaVersion int    `json:"schema_version"`
			GameDirectory string `json:"game_directory"`
		}{}, commonconfig.DecodeOptions{DisallowUnknownFields: true, RejectTrailing: true, AllowBOM: true})
		if err != nil {
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
