// Package bootstrap implements the server discovery phase for the configured client.
// Fields are taken from the current Proto/Net definitions.
package bootstrap

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"bd2server/internal/wire"
)

type Config struct {
	BaseURL     string // Must include the trailing slash, e.g. http://127.0.0.1:8080/game/
	CDNURL      string // Base for /StandaloneWindows64/HD/<version>/...
	GameDataURL string // Leave empty until a healthy GameData archive is available.
	GameDataVer string
	Version     string
	BundleVer   string
}

func (c Config) Validate() error {
	for _, pair := range [][2]string{{"game server", c.BaseURL}, {"CDN", c.CDNURL}} {
		u, err := url.Parse(pair[1])
		if err != nil || u.Scheme != "http" || u.Host == "" {
			return fmt.Errorf("%s needs a valid local HTTP URL: %q", pair[0], pair[1])
		}
	}
	if !strings.HasSuffix(c.BaseURL, "/") {
		return fmt.Errorf("game server URL needs a trailing slash: %q", c.BaseURL)
	}
	if (c.GameDataURL == "") != (c.GameDataVer == "") {
		return fmt.Errorf("GameData URL and version must be provided together")
	}
	if c.Version == "" || c.BundleVer == "" {
		return fmt.Errorf("client and bundle versions must not be empty")
	}
	return nil
}

// MaintenanceInfoResponse: market_info=1, connect_type=3, user_type=4.
// MaintenanceInfo: market_type=1, version=2, bundle_version=3.
func Maintenance(version, bundle string, request []byte) ([]byte, error) {
	_, _, err := wire.Varint(request, 2) // validate MaintenanceInfoRequest.market_type
	if err != nil {
		return nil, err
	}
	// The request's platform market type and the response's market type are
	// different enums, so the latter is not copied from the request.
	market := wire.AppendVarint(nil, 1, 4)
	market = wire.AppendString(market, 2, version)
	market = wire.AppendString(market, 3, bundle)
	answer := wire.AppendBytes(nil, 1, market)
	answer = wire.AppendVarint(answer, 3, 1) // normal connection
	answer = wire.AppendVarint(answer, 4, 1) // nonzero, avoid platform-update branch
	return answer, nil
}

// ServerInfoResponse.info_list=1; nested ServerInfo fields 2/3/5/6/7/8/9.
func ServerInfo(c Config) []byte {
	info := wire.AppendString(nil, 2, c.BaseURL)
	info = wire.AppendString(info, 3, c.CDNURL)
	info = wire.AppendString(info, 5, strings.TrimSuffix(c.BaseURL, "game/")+"logs")
	// Blank chat-server address avoids connecting to an unrelated official host.
	if c.GameDataURL != "" {
		info = wire.AppendString(info, 8, c.GameDataURL)
		info = wire.AppendString(info, 9, c.GameDataVer)
	}
	return wire.AppendBytes(nil, 1, info)
}

// ServerNowTimeResponse.server_time=1 (Unix milliseconds).
func ServerNowTime(now time.Time) []byte {
	return wire.AppendVarint(nil, 1, uint64(now.UnixMilli()))
}

// NoticeInfoResponse has a repeated notice_info=1; no notices is valid.
func NoticeInfo() []byte { return nil }
