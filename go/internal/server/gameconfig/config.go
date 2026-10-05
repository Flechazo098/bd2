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

	"bd2server/internal/server/configfile"
)

const FileName = "game.json"

type Config struct {
	SchemaVersion int             `json:"schema_version"`
	Gacha         GachaConfig     `json:"gacha"`
	Story         StoryConfig     `json:"story"`
	Purchases     PurchasesConfig `json:"purchases"`
}

type PurchasesConfig struct {
	DiamondRecharge DiamondRechargeConfig `json:"diamond_recharge"`
}

// Only paid-diamond recharge has a configurable price. All other cash goods
// use their GameData-derived paid-diamond price.
type DiamondRechargeConfig struct {
	Currency               string `json:"currency"`
	GoldPerPaidDiamond     int64  `json:"gold_per_paid_diamond"`
	DiamondsPerPaidDiamond int64  `json:"diamonds_per_paid_diamond"`
}

// StartPackID is fixed for the server by its first successful initialization.
// TODO: Revisit this restriction if the official story rework merges the old
// story and master packs into one progression. Its release date is unconfirmed;
// it may extend into 2027, so do not infer or implement a merged chain now.
type StoryConfig struct {
	StartPackID int `json:"start_pack_id"`
}

type GachaConfig struct {
	IncludeCollaborationURWeapons bool `json:"include_collaboration_ur_weapons"`
}

func Default() Config {
	return Config{SchemaVersion: 1, Story: StoryConfig{StartPackID: 21}, Purchases: PurchasesConfig{DiamondRecharge: DiamondRechargeConfig{Currency: "free", GoldPerPaidDiamond: 1000, DiamondsPerPaidDiamond: 1}}}
}

// Load generates defaults when missing and strictly validates existing files.
func Load(path string) (Config, error) {
	if err := configfile.Ensure(path, Default()); err != nil {
		return Config{}, fmt.Errorf("game configuration: initialize %s: %w", path, err)
	}
	data, err := os.ReadFile(filepath.Clean(path))
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
	r := c.Purchases.DiamondRecharge
	switch r.Currency {
	case "free", "gold", "diamonds", "ban", "":
	default:
		return fmt.Errorf("purchases.diamond_recharge.currency must be free, gold, diamonds, ban, or an empty string")
	}
	if r.GoldPerPaidDiamond < 1 || r.GoldPerPaidDiamond > 1_000_000_000 || r.DiamondsPerPaidDiamond < 1 || r.DiamondsPerPaidDiamond > 1_000_000_000 {
		return fmt.Errorf("purchases.diamond_recharge conversion rates must be integers between 1 and 1000000000")
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
