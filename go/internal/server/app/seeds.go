package app

import (
	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/server/domain/mail"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/roster/deck"
	"bd2server/internal/server/domain/startergift"
	"bd2server/internal/server/domain/world"
	calendaradapter "bd2server/internal/server/protocol/calendar"
	loginprotocol "bd2server/internal/server/protocol/login"
	"bd2server/internal/server/protocol/staticdata"
	"encoding/json"
	"fmt"
	"path/filepath"
)

type cashPackageLayouts struct {
	Version       string            `json:"version"`
	BundleVersion string            `json:"bundle_version"`
	Layouts       map[string]string `json:"layouts"`
}

type seedCatalog struct {
	login          *loginprotocol.LoginSeed
	starter        *roster.Starter
	defaults       *readonly.Seed
	mailbox        *mail.Starter
	deck           deck.Seed
	world          world.Seed
	packageLayouts cashPackageLayouts
	starterGifts   startergift.Catalog
}

func loadSeeds(c *configuration) (*seedCatalog, error) {
	login, err := loginprotocol.Load(c.accountSeed)
	if err != nil {
		return nil, err
	}
	starter, err := roster.Load(c.playerSeed)
	if err != nil {
		return nil, err
	}
	if login.Version != c.versions.GameVersion || starter.Version != c.versions.GameVersion {
		return nil, errorsVersionMismatch(c.versions.GameVersion, login.Version, starter.Version)
	}
	defaults, err := readonly.Load(c.readonlySeed)
	if err != nil {
		return nil, err
	}
	defaults, err = calendaradapter.ApplyStaticData(c.calendars, defaults)
	if err != nil {
		return nil, err
	}
	mailbox, err := mail.Load(c.mailSeed)
	if err != nil {
		return nil, err
	}
	deckSeed, err := deck.LoadSeed(c.deckSeed)
	if err != nil {
		return nil, err
	}
	worldSeed, err := world.LoadSeed(c.worldSeed)
	if err != nil {
		return nil, err
	}
	layouts, err := commonconfig.Load(filepath.Join(filepath.Dir(c.worldSeed), "cash_package_layouts.json"), cashPackageLayouts{}, commonconfig.DecodeOptions{DisallowUnknownFields: true, RejectTrailing: true})
	if err != nil {
		return nil, fmt.Errorf("load cash package layouts: %w", err)
	}
	if layouts.Version != c.versions.GameVersion {
		return nil, fmt.Errorf("cash package layout game version mismatch: got %s, want %s", layouts.Version, c.versions.GameVersion)
	}
	if layouts.BundleVersion != c.versions.BundleVersion {
		return nil, fmt.Errorf("cash package layout bundle version mismatch: got %s, want %s", layouts.BundleVersion, c.versions.BundleVersion)
	}
	for resource, layout := range layouts.Layouts {
		if resource == "" || layout != "independent" && layout != "relay" {
			return nil, fmt.Errorf("invalid cash package layout %q: %q", resource, layout)
		}
	}
	gifts, err := commonconfig.Load(filepath.Join(filepath.Dir(c.worldSeed), "starter_gifts.json"), startergift.Catalog{}, commonconfig.DecodeOptions{DisallowUnknownFields: true, RejectTrailing: true})
	if err != nil {
		return nil, fmt.Errorf("load starter gifts: %w", err)
	}
	if gifts.Version != c.versions.GameVersion || gifts.GameDataVersion != c.gameDataVersion {
		return nil, fmt.Errorf("starter gift game or GameData version mismatch")
	}
	return &seedCatalog{login: login, starter: starter, defaults: defaults, mailbox: mailbox, deck: deckSeed, world: worldSeed, packageLayouts: layouts, starterGifts: gifts}, nil
}

func cloneSeed[T any](source *T) (*T, error) {
	data, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
