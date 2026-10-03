package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"bd2server/internal/server/account"
	"bd2server/internal/server/authconfig"
	"bd2server/internal/server/deck"
	"bd2server/internal/server/gacha"
	"bd2server/internal/server/gameconfig"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/mail"
	"bd2server/internal/server/player"
	"bd2server/internal/server/readonly"
	"bd2server/internal/server/resourcepolicy"
	"bd2server/internal/server/versionconfig"
)

// preflight validates the candidate binary's immutable configuration without
// opening state.db or claiming writer_epoch. Deployment may run it while the
// old instance is still active, then drain the old writer before activation.
func preflight(args []string) error {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "server data directory")
	versionPath := fs.String("version-config", "", "repository versions.json override")
	authPath := fs.String("authentication-config", "", "authentication.json override")
	resourcePath := fs.String("resource-config", "", "resources.json override")
	gamePath := fs.String("game-config", "", "game.json server gameplay configuration override")
	if err := fs.Parse(args); err != nil {
		return err
	}
	versions, err := versionconfig.Find()
	if *versionPath != "" {
		versions, err = versionconfig.Load(*versionPath)
	}
	if err != nil {
		return err
	}
	versionconfig.Use(versions)
	if *gamePath == "" {
		*gamePath, err = gameconfig.BesideExecutable()
		if err != nil {
			return err
		}
	}
	gameRules, err := gameconfig.Load(*gamePath)
	if err != nil {
		return err
	}
	if *authPath == "" {
		*authPath, err = authconfig.BesideExecutable()
		if err != nil {
			return err
		}
	}
	authentication, err := authconfig.Load(*authPath)
	if err != nil {
		return err
	}
	runtime, err := authentication.ResolveEnvironment()
	if err != nil {
		return err
	}
	clear(runtime.MasterKey)
	if *resourcePath == "" {
		*resourcePath, err = resourcepolicy.BesideExecutable()
		if err != nil {
			return err
		}
	}
	if _, err := resourcepolicy.Load(*resourcePath); err != nil {
		return err
	}
	if *dataDir == "" {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		*dataDir = filepath.Join(filepath.Dir(executable), "data")
	}
	gameData := filepath.Join(filepath.Clean(*dataDir), "resources", "GameData")
	if _, _, err := gamedata.Ensure(context.Background(), nil, gameData, versions.GameDataVersion, resourcepolicy.OfficialGameDataURL); err != nil {
		return fmt.Errorf("preflight GameData: %w", err)
	}
	seedRoot := versions.Resolve(versions.SeedDirectory)
	login, err := account.Load(filepath.Join(seedRoot, "login_user.json"))
	if err != nil {
		return err
	}
	starter, err := player.Load(filepath.Join(seedRoot, "starter_player.json"))
	if err != nil {
		return err
	}
	if login.Version != versions.GameVersion || starter.Version != versions.GameVersion {
		return errorsVersionMismatch(versions.GameVersion, login.Version, starter.Version)
	}
	if _, err := mail.Load(filepath.Join(seedRoot, "mail.json")); err != nil {
		return err
	}
	if _, err := deck.LoadSeed(filepath.Join(seedRoot, "decks.json")); err != nil {
		return err
	}
	if _, err := readonly.Load(filepath.Join(seedRoot, "readonly.json")); err != nil {
		return err
	}
	schedule, err := gacha.LoadScheduleSeed(filepath.Join(seedRoot, "gacha_schedule.json"), versions.GameVersion)
	if err != nil {
		return err
	}
	var groups, steps []uint64
	for _, window := range schedule.Schedules {
		groups = append(groups, window.GroupID)
	}
	for _, window := range schedule.StepUps {
		steps = append(steps, window.GroupID)
	}
	_, equipment, err := gamedata.LoadActiveGachaForSchedules(gameData, versions.GameDataVersion, groups, steps)
	if err != nil {
		return err
	}
	if gameRules.Gacha.IncludeCollaborationURWeapons {
		if err := equipment.IncludeCollaborationURWeapons(gameData, versions.GameDataVersion); err != nil {
			return fmt.Errorf("preflight collaboration UR weapon game rule: %w", err)
		}
	}
	if _, err := gamedata.LoadFirstGacha(gameData, versions.GameDataVersion); err != nil {
		return err
	}
	if _, err := gamedata.LoadLimitedCostumes(gameData, versions.GameDataVersion); err != nil {
		return err
	}
	if _, err := gamedata.LoadCostumeBurstDesign(gameData, versions.GameDataVersion); err != nil {
		return err
	}
	return nil
}

func errorsVersionMismatch(want, login, player string) error {
	return fmt.Errorf("game version %s requires matching account and player seeds (got %s and %s)", want, login, player)
}
