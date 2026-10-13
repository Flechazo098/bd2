// Package resourcepolicy loads the server-authoritative client resource policy.
package resourcepolicy

import (
	"errors"
	"net"
	"net/url"
	"strings"

	commonconfig "bd2server/internal/common/config"
)

const (
	FileName              = "resources.json"
	ModeOfficial          = "official"
	ModeServer            = "server"
	OfficialServerDataURL = "https://dl.bd2.pmang.cloud/ServerData"
	OfficialGameDataURL   = "https://dl.bd2.pmang.cloud/GameData"
)

// Config is private server configuration. It contains no credentials, but it
// remains server-authoritative so a client cannot silently select resources
// that disagree with the server operator's policy.
type Config struct {
	Mode          string `json:"mode"`
	ServerDataURL string `json:"server_data_url,omitempty"`
	GameDataURL   string `json:"game_data_url,omitempty"`
}

// Public is the complete resource policy returned to a client. Versions come
// from the server's validated versions.json rather than client input.
type Public struct {
	Mode            string `json:"mode"`
	ServerDataURL   string `json:"server_data_url"`
	GameDataURL     string `json:"game_data_url"`
	BundleVersion   string `json:"bundle_version"`
	GameDataVersion string `json:"game_data_version"`
}

func Default() Config {
	return Config{
		Mode:          ModeOfficial,
		ServerDataURL: OfficialServerDataURL,
		GameDataURL:   OfficialGameDataURL,
	}
}

// Document is resources.json. Unknown fields and trailing JSON are rejected
// so a misspelled URL key cannot silently fall back to another CDN.
var Document = commonconfig.Document[Config]{
	Name:     "resources",
	FileName: FileName,
	Defaults: Default,
	Base:     func() Config { return Config{} },
	Decode:   commonconfig.DecodeOptions{DisallowUnknownFields: true, RejectTrailing: true},
	Schema: commonconfig.Schema{
		Title:   "客户端资源",
		Summary: "客户端下载 ServerData 与 GameData 的来源。修改后重启服务端生效；版本号来自 versions.json，不在此配置。",
		Sections: []commonconfig.Section{{ID: "source", Title: "资源来源", Fields: []commonconfig.Field{
			{Path: "mode", Kind: commonconfig.KindString, Label: "模式", Options: []commonconfig.Option{
				{Value: ModeOfficial, Label: "官方 CDN", Help: "使用当前版本的官方资源地址。"},
				{Value: ModeServer, Label: "自建资源源", Help: "从服主提供的静态 CDN 或缓存反代下载。"},
			}},
			{Path: "server_data_url", Kind: commonconfig.KindString, Label: "ServerData 地址", Format: "url", Placeholder: OfficialServerDataURL,
				When: &commonconfig.Condition{Path: "mode", In: []any{ModeServer}}, Help: "绝对 HTTP(S) 地址，末尾不带斜杠；除回环地址外必须使用 HTTPS。"},
			{Path: "game_data_url", Kind: commonconfig.KindString, Label: "GameData 地址", Format: "url", Placeholder: OfficialGameDataURL,
				When: &commonconfig.Condition{Path: "mode", In: []any{ModeServer}}, Help: "绝对 HTTP(S) 地址，末尾不带斜杠；除回环地址外必须使用 HTTPS。"},
		}}},
	},
	Rules: rules,
}

func Load(path string) (Config, error) { return Document.Load(path) }

func (c Config) Validate() error { return Document.Validate(c) }

func rules(c Config) commonconfig.Problems {
	var problems commonconfig.Problems
	switch c.Mode {
	case ModeOfficial:
		if c.ServerDataURL != "" && c.ServerDataURL != OfficialServerDataURL {
			problems.Add("server_data_url", commonconfig.RuleConflict, "official mode must use the versioned official resource origin")
		}
		if c.GameDataURL != "" && c.GameDataURL != OfficialGameDataURL {
			problems.Add("game_data_url", commonconfig.RuleConflict, "official mode must use the versioned official resource origin")
		}
	case ModeServer:
		if c.ServerDataURL == "" {
			problems.Add("server_data_url", commonconfig.RuleRequired, "is required in server mode")
		}
		if c.GameDataURL == "" {
			problems.Add("game_data_url", commonconfig.RuleRequired, "is required in server mode")
		}
	default:
		return problems
	}
	for _, origin := range [...]struct{ path, value string }{{"server_data_url", c.serverDataURL()}, {"game_data_url", c.gameDataURL()}} {
		if origin.value == "" {
			continue
		}
		if err := validateBaseURL(origin.value); err != nil {
			problems.Add(origin.path, commonconfig.RuleFormat, "%s", err.Error())
		}
	}
	return problems
}

func (c Config) Public(bundleVersion, gameDataVersion string) Public {
	return Public{
		Mode:            c.Mode,
		ServerDataURL:   c.serverDataURL(),
		GameDataURL:     c.gameDataURL(),
		BundleVersion:   bundleVersion,
		GameDataVersion: gameDataVersion,
	}
}

func (c Config) serverDataURL() string {
	if c.Mode == ModeOfficial && c.ServerDataURL == "" {
		return OfficialServerDataURL
	}
	return c.ServerDataURL
}

func (c Config) gameDataURL() string {
	if c.Mode == ModeOfficial && c.GameDataURL == "" {
		return OfficialGameDataURL
	}
	return c.GameDataURL
}

func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if strings.HasSuffix(u.Path, "/") {
		return errors.New("must not have a trailing slash")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return errors.New("must use HTTPS outside loopback development")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
