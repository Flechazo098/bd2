package app

import (
	"bd2server/internal/server/design/gamedata"
	"errors"
	"fmt"
)

// Preflight validates the same shared rules as Serve without opening player or authentication databases.
func Preflight(args []string) (preflightErr error) {
	defer func() { preflightErr = errors.Join(preflightErr, gamedata.CloseDatabaseCache()) }()
	config, err := loadConfiguration(args)
	if err != nil {
		return err
	}
	defer clear(config.authRuntime.MasterKey)
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
