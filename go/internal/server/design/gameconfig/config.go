// Package gameconfig loads server-authoritative gameplay rules.
package gameconfig

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	commonconfig "bd2server/internal/common/config"
)

const FileName = "game.json"

type Config struct {
	Realtime       RealtimeConfig       `json:"realtime"`
	SchemaVersion  int                  `json:"schema_version"`
	Gacha          GachaConfig          `json:"gacha"`
	Story          StoryConfig          `json:"story"`
	Purchases      PurchasesConfig      `json:"purchases"`
	TextModeration TextModerationConfig `json:"text_moderation"`
	SpineRecording SpineRecordingConfig `json:"spine_recording"`
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

// TextScopes are the input entry points currently connected to moderation.
var TextScopes = []string{
	"nickname", "greeting", "master_title", "preset_name", "equipment_preset_name", "equipment_mark",
	"monster_hunt_preset_name", "guild_raid_preset_name", "colosseum_preset_name",
	"guild_name", "guild_message", "guild_notice", "room_name", "chat",
	"spine_record_name",
}

func DefaultTextModeration() TextModerationConfig {
	return TextModerationConfig{RuleFiles: []string{}, Provider: TextModerationProviderConfig{
		Type: "off", APIKeyHeader: "Authorization", Scopes: append([]string(nil), TextScopes...),
		TimeoutMS: 1500, FailurePolicy: "deny", SeverityThreshold: 4,
	}}
}

// Validate checks the moderation group as it would appear in game.json.
func (c TextModerationConfig) Validate() error {
	config := Default()
	config.TextModeration = c
	return Document.Validate(config)
}

func textModerationRules(c TextModerationConfig, problems *commonconfig.Problems) {
	for i, file := range c.RuleFiles {
		if file == "" {
			problems.Add(indexed("text_moderation.rule_files", i), commonconfig.RuleRequired, "must be a nonempty path")
		}
	}
	p := c.Provider
	if p.Type != "off" {
		if p.Endpoint == "" {
			problems.Add("text_moderation.provider.endpoint", commonconfig.RuleRequired, "is required by an active provider")
		}
		if len(p.Scopes) == 0 {
			problems.Add("text_moderation.provider.scopes", commonconfig.RuleRequired, "is required by an active provider")
		}
	}
	if p.Type == "azure_content_safety" && p.APIKeyEnv == "" {
		problems.Add("text_moderation.provider.api_key_env", commonconfig.RuleRequired, "is required by the Azure provider")
	}
	for i, scope := range p.Scopes {
		if !ValidTextScope(scope) {
			problems.Add(indexed("text_moderation.provider.scopes", i), commonconfig.RuleFormat, "must be a lowercase identifier")
		}
	}
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

// Document is game.json. Omitted gameplay groups keep their defaults, but the
// format version is required even when every group is omitted.
var Document = commonconfig.Document[Config]{
	Name:     "game",
	FileName: FileName,
	Defaults: Default,
	Base: func() Config {
		defaults := Default()
		defaults.SchemaVersion = 0
		return defaults
	},
	Decode: commonconfig.DecodeOptions{DisallowUnknownFields: true, RejectTrailing: true, RejectNull: true},
	Schema: schema,
	Rules: func(c Config) commonconfig.Problems {
		var problems commonconfig.Problems
		realtimeRules(c.Realtime, &problems)
		textModerationRules(c.TextModeration, &problems)
		spineRecordingRules(c.SpineRecording, &problems)
		return problems
	},
	Advice: func(c Config, path string) commonconfig.Problems {
		var advice commonconfig.Problems
		missing := func(field, file string) {
			if file == "" {
				return
			}
			if _, err := os.Stat(ResolvePath(path, file)); err != nil {
				advice.Add(field, commonconfig.RuleMissingFile, "%s is not readable by this server process", file)
			}
		}
		for i, file := range c.TextModeration.RuleFiles {
			missing(indexed("text_moderation.rule_files", i), file)
		}
		if c.Realtime.Enabled && !c.Realtime.DevelopmentPlainLoopback {
			missing("realtime.tls_cert_file", c.Realtime.TLSCertFile)
			missing("realtime.tls_key_file", c.Realtime.TLSKeyFile)
		}
		p := c.TextModeration.Provider
		if p.Type != "off" && p.APIKeyEnv != "" && strings.TrimSpace(os.Getenv(p.APIKeyEnv)) == "" {
			advice.Add("text_moderation.provider.api_key_env", commonconfig.RuleEnvironment, "environment variable %s is empty in this server process", p.APIKeyEnv)
		}
		return advice
	},
}

// ResolvePath resolves a file referenced by game.json relative to its directory.
func ResolvePath(configPath, file string) string {
	if filepath.IsAbs(file) {
		return filepath.Clean(file)
	}
	return filepath.Join(filepath.Dir(configPath), file)
}

// Load generates defaults when missing and strictly validates existing files.
func Load(path string) (Config, error) { return Document.Load(path) }

func (c Config) Validate() error { return Document.Validate(c) }

func indexed(path string, index int) string { return path + "[" + strconv.Itoa(index) + "]" }
