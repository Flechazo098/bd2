// Package authconfig loads the server-authoritative authentication policy.
package authconfig

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	commonconfig "bd2server/internal/common/config"
)

const FileName = "authentication.json"

type Config struct {
	Mode         string                    `json:"mode"`
	PublicURL    string                    `json:"public_url,omitempty"`
	MasterKeyEnv string                    `json:"master_key_env,omitempty"`
	Providers    map[string]ProviderConfig `json:"providers,omitempty"`
	Session      SessionConfig             `json:"session"`
}

type ProviderConfig struct {
	ClientID        string `json:"client_id"`
	ClientSecretEnv string `json:"client_secret_env"`
}

type SessionConfig struct {
	AccessTTL            string `json:"access_ttl,omitempty"`
	RefreshTTL           string `json:"refresh_ttl,omitempty"`
	DeviceTransactionTTL string `json:"device_transaction_ttl,omitempty"`
}

type Runtime struct {
	Config
	PublicURLParsed *url.URL
	MasterKey       []byte
	ProviderSecrets map[string]string
	AccessTTL       time.Duration
	RefreshTTL      time.Duration
	DeviceTTL       time.Duration
}

type Public struct {
	Mode      string   `json:"mode"`
	Providers []string `json:"providers"`
}

func Default() Config { return Config{Mode: "local"} }

var oauth = &commonconfig.Condition{Path: "mode", In: []any{"oauth"}}

// Document is authentication.json. Omitted values stay empty rather than
// taking defaults so that a local-mode file cannot silently gain OAuth.
var Document = commonconfig.Document[Config]{
	Name:     "authentication",
	FileName: FileName,
	Defaults: Default,
	Base:     func() Config { return Config{} },
	Decode:   commonconfig.DecodeOptions{DisallowUnknownFields: true, RejectTrailing: true},
	Schema: commonconfig.Schema{
		Title:   "登录与认证",
		Summary: "玩家登录方式。修改后重启服务端生效；密钥只从环境变量读取，不写入此文件。",
		Sections: []commonconfig.Section{
			{ID: "mode", Title: "认证模式", Fields: []commonconfig.Field{
				{Path: "mode", Kind: commonconfig.KindString, Label: "模式", Options: []commonconfig.Option{
					{Value: "local", Label: "本地单人", Help: "无需登录，只有一个本地玩家。"},
					{Value: "oauth", Label: "OAuth 联机", Help: "玩家通过 Discord 或 Google 登录，服务器可供多人使用。"},
				}},
				{Path: "public_url", Kind: commonconfig.KindString, Label: "公开地址", Format: "origin", Placeholder: "https://game.example.com", When: oauth,
					Help: "玩家和 OAuth 回调访问的根地址，不带路径；除 localhost 外必须使用 HTTPS。"},
				{Path: "master_key_env", Kind: commonconfig.KindString, Label: "主密钥环境变量", Format: "env", When: oauth,
					Help: "该环境变量须为 base64 编码的 32 字节密钥，用于加密登录数据。"},
			}},
			{ID: "providers", Title: "登录提供方", Help: "回调地址为 公开地址/auth/<提供方>/callback。", Fields: []commonconfig.Field{
				{Path: "providers", Kind: commonconfig.KindMap, Label: "启用的提供方", When: oauth, Keys: []commonconfig.Option{
					{Value: "discord", Label: "Discord"}, {Value: "google", Label: "Google"},
				}},
				{Path: "providers.*.client_id", Kind: commonconfig.KindString, Label: "Client ID", When: oauth},
				{Path: "providers.*.client_secret_env", Kind: commonconfig.KindString, Label: "Client secret 环境变量", Format: "env", When: oauth},
			}},
			{ID: "session", Title: "会话时长", Help: "使用 Go 时长格式，例如 15m、720h。留空使用默认值。", Fields: []commonconfig.Field{
				{Path: "session.access_ttl", Kind: commonconfig.KindString, Label: "访问令牌有效期", Format: "duration", Placeholder: "15m", Advanced: true},
				{Path: "session.refresh_ttl", Kind: commonconfig.KindString, Label: "刷新令牌有效期", Format: "duration", Placeholder: "720h", Advanced: true},
				{Path: "session.device_transaction_ttl", Kind: commonconfig.KindString, Label: "登录事务有效期", Format: "duration", Placeholder: "10m", Advanced: true},
			}},
		},
	},
	Rules: rules,
	Advice: func(c Config, _ string) commonconfig.Problems {
		var advice commonconfig.Problems
		if c.Mode != "oauth" {
			return advice
		}
		if c.MasterKeyEnv != "" {
			if key, err := base64.StdEncoding.DecodeString(os.Getenv(c.MasterKeyEnv)); err != nil || len(key) != 32 {
				advice.Add("master_key_env", commonconfig.RuleEnvironment, "environment variable %s is not a base64-encoded 32-byte key in this server process", c.MasterKeyEnv)
			}
		}
		for _, name := range providerNames(c) {
			if env := c.Providers[name].ClientSecretEnv; env != "" && strings.TrimSpace(os.Getenv(env)) == "" {
				advice.Add("providers."+name+".client_secret_env", commonconfig.RuleEnvironment, "environment variable %s is empty in this server process", env)
			}
		}
		return advice
	},
}

func rules(c Config) commonconfig.Problems {
	var problems commonconfig.Problems
	if c.Mode == "local" {
		if c.PublicURL != "" {
			problems.Add("public_url", commonconfig.RuleConflict, "local mode must not configure OAuth")
		}
		if c.MasterKeyEnv != "" {
			problems.Add("master_key_env", commonconfig.RuleConflict, "local mode must not configure OAuth")
		}
		if len(c.Providers) != 0 {
			problems.Add("providers", commonconfig.RuleConflict, "local mode must not configure OAuth")
		}
	}
	if c.Mode == "oauth" {
		if c.MasterKeyEnv == "" {
			problems.Add("master_key_env", commonconfig.RuleRequired, "is required in oauth mode")
		}
		if len(c.Providers) == 0 {
			problems.Add("providers", commonconfig.RuleRequired, "oauth mode requires at least one provider")
		}
		if c.PublicURL == "" {
			problems.Add("public_url", commonconfig.RuleRequired, "is required in oauth mode")
		} else if publicURL, err := url.Parse(c.PublicURL); err != nil || publicURL.Host == "" || publicURL.User != nil || publicURL.RawQuery != "" || publicURL.Fragment != "" || publicURL.Path != "" {
			problems.Add("public_url", commonconfig.RuleFormat, "must be an absolute origin without path, query, fragment, or user info")
		} else if localhost := publicURL.Hostname() == "127.0.0.1" || publicURL.Hostname() == "localhost" || publicURL.Hostname() == "::1"; publicURL.Scheme != "https" && (!localhost || publicURL.Scheme != "http") {
			problems.Add("public_url", commonconfig.RuleFormat, "must use HTTPS except on localhost")
		}
		for _, name := range providerNames(c) {
			provider := c.Providers[name]
			if provider.ClientID == "" {
				problems.Add("providers."+name+".client_id", commonconfig.RuleRequired, "is required")
			}
			if provider.ClientSecretEnv == "" {
				problems.Add("providers."+name+".client_secret_env", commonconfig.RuleRequired, "is required")
			}
		}
	}
	for _, ttl := range [...]struct{ path, value string }{
		{"session.access_ttl", c.Session.AccessTTL}, {"session.refresh_ttl", c.Session.RefreshTTL}, {"session.device_transaction_ttl", c.Session.DeviceTransactionTTL},
	} {
		if ttl.value != "" {
			if value, err := time.ParseDuration(ttl.value); err != nil || value <= 0 {
				problems.Add(ttl.path, commonconfig.RuleFormat, "must be a positive Go duration")
			}
		}
	}
	return problems
}

func providerNames(c Config) []string {
	var names []string
	for _, name := range []string{"discord", "google"} {
		if _, ok := c.Providers[name]; ok {
			names = append(names, name)
		}
	}
	return names
}

func Load(path string) (Config, error) { return Document.Load(path) }

func (c Config) Validate() error { return Document.Validate(c) }

func (c Config) ResolveEnvironment() (Runtime, error) {
	if err := c.Validate(); err != nil {
		return Runtime{}, err
	}
	runtime := Runtime{Config: c, ProviderSecrets: make(map[string]string), AccessTTL: 15 * time.Minute, RefreshTTL: 30 * 24 * time.Hour, DeviceTTL: 10 * time.Minute}
	if c.Session.AccessTTL != "" {
		runtime.AccessTTL, _ = time.ParseDuration(c.Session.AccessTTL)
	}
	if c.Session.RefreshTTL != "" {
		runtime.RefreshTTL, _ = time.ParseDuration(c.Session.RefreshTTL)
	}
	if c.Session.DeviceTransactionTTL != "" {
		runtime.DeviceTTL, _ = time.ParseDuration(c.Session.DeviceTransactionTTL)
	}
	if c.Mode == "local" {
		return runtime, nil
	}
	runtime.PublicURLParsed, _ = url.Parse(c.PublicURL)
	key, err := base64.StdEncoding.DecodeString(os.Getenv(c.MasterKeyEnv))
	if err != nil || len(key) != 32 {
		return Runtime{}, fmt.Errorf("authconfig: %s must contain a base64-encoded 32-byte key", c.MasterKeyEnv)
	}
	runtime.MasterKey = key
	for name, provider := range c.Providers {
		secret := os.Getenv(provider.ClientSecretEnv)
		if strings.TrimSpace(secret) == "" {
			return Runtime{}, fmt.Errorf("authconfig: provider %s secret environment %s is empty", name, provider.ClientSecretEnv)
		}
		runtime.ProviderSecrets[name] = secret
	}
	return runtime, nil
}

func (c Config) Public() Public {
	return Public{Mode: c.Mode, Providers: append([]string{}, providerNames(c)...)}
}
