package app

import (
	"context"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/server/admin"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/gateway/auth"
	"bd2server/internal/server/protocol/wire"
	identitystore "bd2server/internal/server/storage/identity"
)

// openConsole assembles the administration website from the running server's
// configuration files, identity directory and GameData.
func openConsole(c *configuration, design *designCatalog, seeds *seedCatalog, authService *auth.Service, authStore *identitystore.Store, instanceID string) (*admin.Console, *admin.Store, error) {
	store, err := admin.OpenStore(filepath.Join(c.stateDirectory, "admin.db"))
	if err != nil {
		return nil, nil, err
	}
	developmentSettings, _, err := inventory.DevelopmentSettingsDocument.Open(c.devToolsConfig)
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	players := func(ctx context.Context) ([]admin.Player, error) {
		if authStore == nil {
			owner, _, err := wire.Varint(seeds.login.UserInfo, 1)
			if err != nil {
				return nil, err
			}
			return []admin.Player{{AccountID: "local-owner", UID: strconv.FormatUint(owner, 10), Name: "本地玩家", Active: true}}, nil
		}
		profiles, err := authStore.Players(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]admin.Player, len(profiles))
		for i, profile := range profiles {
			out[i] = admin.Player{AccountID: profile.AccountID, UID: strconv.FormatInt(profile.OwnerIndex, 10), Name: profile.UserID, Active: profile.Active}
		}
		return out, nil
	}
	catalog := sync.OnceValues(func() ([]gamedata.MailCatalogItem, error) {
		items, err := gamedata.LoadMailCatalog(c.gameData, c.gameDataVersion)
		if err == nil {
			gamedata.SortMailCatalog(items)
		}
		return items, err
	})
	started := time.Now().UTC()
	options := admin.Options{
		Store: store, Players: players, Catalog: catalog, MailSpool: c.mailGrantSpool, Settings: developmentSettings,
		Configuration: commonconfig.NewRegistry(c.gameFile, c.authFile, c.resourceFile, c.updateFile),
		Secure:        c.authentication.Mode == "oauth" && c.authRuntime.PublicURLParsed != nil && c.authRuntime.PublicURLParsed.Scheme == "https",
		Server: func() any {
			return map[string]any{
				"server_version": c.versions.ServerVersion, "game_version": c.versions.GameVersion, "game_data_version": c.gameDataVersion,
				"bundle_version": c.versions.BundleVersion, "auth_mode": c.authentication.Mode, "listen": c.listen, "data_dir": c.dataDir,
				"instance_id": instanceID, "started_at": started, "mail_spool": c.mailGrantSpool, "dev_tools": c.devToolsConfig,
			}
		},
	}
	if authService != nil {
		options.Identity = authService
	}
	console, err := admin.New(options)
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	return console, store, nil
}

// consoleOrigin is the externally visible origin for printed console links.
func consoleOrigin(c *configuration) string {
	if c.authentication.Mode == "oauth" {
		return c.authentication.PublicURL
	}
	host, port, err := net.SplitHostPort(c.listen)
	if err != nil {
		return "http://" + c.listen
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}).String()
}
