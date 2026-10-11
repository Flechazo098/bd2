package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/platform/textmoderation"
	"errors"
	"fmt"
	"path/filepath"
)

// Preflight validates the same shared rules as Serve without opening player or authentication databases.
func Preflight(args []string) (preflightErr error) {
	defer func() { preflightErr = errors.Join(preflightErr, gamedata.CloseDatabaseCache()) }()
	config, err := loadConfiguration(args)
	if err != nil {
		return err
	}
	defer clear(config.authRuntime.MasterKey)
	moderator, err := textmoderation.New(config.gameRules.TextModeration, filepath.Dir(config.gameConfigPath))
	if err != nil {
		return fmt.Errorf("initialize text moderation: %w", err)
	}
	defer func() { preflightErr = errors.Join(preflightErr, moderator.Close()) }()
	seeds, err := loadSeeds(config)
	if err != nil {
		return err
	}
	_, err = loadDesign(config, seeds)
	return err
}

func errorsVersionMismatch(want, login, player string) error {
	return fmt.Errorf("game version %s requires matching account and player seeds (got %s and %s)", want, login, player)
}
