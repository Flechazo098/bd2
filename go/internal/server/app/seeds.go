package app

import (
	"bd2server/internal/server/domain/mail"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/roster/deck"
	"bd2server/internal/server/domain/world"
	calendaradapter "bd2server/internal/server/protocol/calendar"
	loginprotocol "bd2server/internal/server/protocol/login"
	"bd2server/internal/server/protocol/staticdata"
	"encoding/json"
)

type seedCatalog struct {
	login    *loginprotocol.LoginSeed
	starter  *roster.Starter
	defaults *readonly.Seed
	mailbox  *mail.Starter
	deck     deck.Seed
	world    world.Seed
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
	return &seedCatalog{login: login, starter: starter, defaults: defaults, mailbox: mailbox, deck: deckSeed, world: worldSeed}, nil
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
