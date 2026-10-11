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
	"slices"

	"bd2server/internal/server/platform/configfile"
)

const FileName = "game.json"

type Config struct {
	Realtime       RealtimeConfig       `json:"realtime"`
	SchemaVersion  int                  `json:"schema_version"`
	Gacha          GachaConfig          `json:"gacha"`
	Story          StoryConfig          `json:"story"`
	Purchases      PurchasesConfig      `json:"purchases"`
	TextModeration TextModerationConfig `json:"text_moderation"`
}

type TextModerationConfig struct {
	RuleFiles []string                     `json:"rule_files"`
	Provider  TextModerationProviderConfig `json:"provider"`
}

type TextModerationProviderConfig struct {
	Type              string   `json:"type"`
	Endpoint          string   `json:"endpoint"`
	APIKeyEnv         string   `json:"api_key_env"`
	APIKeyHeader      string   `json:"api_key_header"`
	Scopes            []string `json:"scopes"`
	TimeoutMS         int      `json:"timeout_ms"`
	FailurePolicy     string   `json:"failure_policy"`
	SeverityThreshold int      `json:"severity_threshold"`
}

func DefaultTextModeration() TextModerationConfig {
	return TextModerationConfig{RuleFiles: []string{}, Provider: TextModerationProviderConfig{
		Type: "off", APIKeyHeader: "Authorization", Scopes: []string{
			"nickname", "greeting", "master_title", "preset_name", "equipment_preset_name", "equipment_mark",
			"monster_hunt_preset_name", "guild_raid_preset_name", "colosseum_preset_name",
			"guild_name", "guild_message", "guild_notice", "chat",
		},
		TimeoutMS: 1500, FailurePolicy: "deny", SeverityThreshold: 4,
	}}
}

func (c TextModerationConfig) Validate() error {
	if slices.Contains(c.RuleFiles, "") {
		return fmt.Errorf("text_moderation.rule_files must contain nonempty paths")
	}
	p := c.Provider
	if p.Type != "off" && p.Type != "http_json" && p.Type != "azure_content_safety" {
		return fmt.Errorf("text_moderation.provider.type must be off, http_json, or azure_content_safety")
	}
	if p.TimeoutMS < 10 || p.TimeoutMS > 30000 {
		return fmt.Errorf("text_moderation.provider.timeout_ms must be between 10 and 30000")
	}
	if p.FailurePolicy != "allow" && p.FailurePolicy != "deny" {
		return fmt.Errorf("text_moderation.provider.failure_policy must be allow or deny")
	}
	if p.SeverityThreshold < 0 || p.SeverityThreshold > 7 {
		return fmt.Errorf("text_moderation.provider.severity_threshold must be between 0 and 7")
	}
	if p.Type != "off" && (p.Endpoint == "" || len(p.Scopes) == 0) {
		return fmt.Errorf("text_moderation active provider requires endpoint and scopes")
	}
	if p.Type == "azure_content_safety" && p.APIKeyEnv == "" {
		return fmt.Errorf("text_moderation Azure provider requires api_key_env")
	}
	seen := map[string]bool{}
	for _, scope := range p.Scopes {
		if !ValidTextScope(scope) || seen[scope] {
			return fmt.Errorf("text_moderation.provider.scopes must contain unique lowercase identifiers")
		}
		seen[scope] = true
	}
	return nil
}

func ValidTextScope(scope string) bool {
	if len(scope) == 0 || len(scope) > 64 || scope[0] < 'a' || scope[0] > 'z' {
		return false
	}
	for _, r := range scope {
		allowed := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_'
		if !allowed {
			return false
		}
	}
	return true
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
	return Config{SchemaVersion: 1, Story: StoryConfig{StartPackID: 21}, Purchases: PurchasesConfig{DiamondRecharge: DiamondRechargeConfig{Currency: "free", GoldPerPaidDiamond: 1000, DiamondsPerPaidDiamond: 1}}, TextModeration: DefaultTextModeration()}
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
		return slices.ContainsFunc(value, containsNull)
	}
	return false
}

func (c Config) Validate() error {
	if err := c.Realtime.Validate(); err != nil {
		return err
	}
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
	return c.TextModeration.Validate()
}

func BesideExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("game configuration: resolve server executable: %w", err)
	}
	return filepath.Join(filepath.Dir(executable), FileName), nil
}
