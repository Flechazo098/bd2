package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"path/filepath"

	controlv1 "bd2server/gen/state/control/v1"
	"bd2server/internal/gacha"
	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/statebridge"
)

func stateCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: bd2server state <check|migrate-v1-v2|repair> [options]")
	}
	switch args[0] {
	case "check":
		return stateCheckCommand(args[1:])
	case "migrate-v1-v2":
		return stateMigrateV1ToV2Command(args[1:])
	case "repair":
		return stateRepairCommand(args[1:])
	default:
		return errors.New("usage: bd2server state <check|migrate-v1-v2|repair> [options]")
	}
}

func stateCheckCommand(args []string) error {
	fs := flag.NewFlagSet("state check", flag.ContinueOnError)
	stateDir := fs.String("state", filepath.FromSlash("../data/state"), "directory containing the nine JSON state files")
	tool := fs.String("tool", "", "optional Haskell state tool override")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resolved, cleanup, err := statebridge.ResolveTool(*tool)
	if err != nil {
		return err
	}
	defer cleanup()
	result, err := statebridge.Check(context.Background(), filepath.Clean(*stateDir), resolved)
	if err != nil {
		return err
	}
	fmt.Printf("state SHA-256: %s\n", hex.EncodeToString(result.SourceSHA256[:]))
	var errorsFound int
	for _, violation := range result.Violations {
		severity := violation.Severity.String()
		fmt.Printf("%s %s %s: %s", severity, violation.Code, filepath.Join(violation.Path...), violation.Message)
		if len(violation.RelatedIds) != 0 {
			fmt.Printf(" ids=%v", violation.RelatedIds)
		}
		fmt.Println()
		if violation.Severity == controlv1.Severity_SEVERITY_ERROR {
			errorsFound++
		}
	}
	if errorsFound != 0 {
		return fmt.Errorf("state check rejected with %d error(s)", errorsFound)
	}
	fmt.Printf("state check passed (%d warning(s))\n", len(result.Violations))
	return nil
}

func stateMigrateV1ToV2Command(args []string) error {
	fs := flag.NewFlagSet("state migrate-v1-v2", flag.ContinueOnError)
	stateDir := fs.String("state", filepath.FromSlash("../data/state"), "directory containing the nine JSON state files")
	tool := fs.String("tool", "", "optional Haskell state tool override")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resolved, cleanup, err := statebridge.ResolveTool(*tool)
	if err != nil {
		return err
	}
	defer cleanup()
	result, err := statebridge.MigrateV1ToV2(context.Background(), filepath.Clean(*stateDir), resolved)
	if err != nil {
		return err
	}
	fmt.Printf("source JSON SHA-256: %s\n", hex.EncodeToString(result.SourceSHA256[:]))
	for _, violation := range result.Violations {
		fmt.Printf("%s %s %s: %s\n", violation.Severity.String(), violation.Code, filepath.Join(violation.Path...), violation.Message)
	}
	if result.Target == nil {
		return errors.New("state migration rejected; no files were changed")
	}
	fmt.Printf("target protobuf SHA-256: %s\n", hex.EncodeToString(result.TargetSHA256[:]))
	fmt.Printf("V1 -> V2 migration passed: %d character(s), %d costume(s); no files were changed\n",
		len(result.Target.Roster.Characters), len(result.Target.Roster.Costumes))
	return nil
}

func stateRepairCommand(args []string) error {
	fs := flag.NewFlagSet("state repair", flag.ContinueOnError)
	stateDir := fs.String("state", filepath.FromSlash("../data/state"), "directory containing the nine JSON state files")
	tool := fs.String("tool", "", "optional Haskell state tool override")
	gameData := fs.String("game-data", "", "versioned GameData root (required)")
	gameDataVersion := fs.String("game-data-version", "", "validated GameData version (required)")
	playerSeed := fs.String("player-seed", `seed\v2_34_13\starter_player.json`, "versioned starter player seed")
	apply := fs.Bool("apply", false, "install a changed repair result with rollback protection")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *gameData == "" || *gameDataVersion == "" {
		return errors.New("state repair requires --game-data and --game-data-version")
	}
	resolved, cleanup, err := statebridge.ResolveTool(*tool)
	if err != nil {
		return err
	}
	defer cleanup()
	catalog, err := gamedata.LoadRegularCostumeGacha(filepath.Clean(*gameData), *gameDataVersion)
	if err != nil {
		return err
	}
	starter, err := player.Load(filepath.Clean(*playerSeed))
	if err != nil {
		return err
	}
	groupID, gachaIDs := gacha.StepUpMigrationFact()
	validation, err := statebridge.BuildValidationContext(*gameDataVersion, catalog, starter, groupID, gachaIDs)
	if err != nil {
		return err
	}
	result, err := statebridge.Repair(context.Background(), filepath.Clean(*stateDir), resolved, validation)
	if err != nil {
		return err
	}
	fmt.Printf("source JSON SHA-256: %s\n", hex.EncodeToString(result.SourceSHA256[:]))
	if result.Target == nil {
		return violationsError("state repair rejected", result.Violations)
	}
	fmt.Printf("target protobuf SHA-256: %s\nchanged: %t\n", hex.EncodeToString(result.TargetSHA256[:]), result.Changed)
	if result.Changed && *apply {
		if err := statebridge.InstallRepair(filepath.Clean(*stateDir), result); err != nil {
			return err
		}
		fmt.Println("repair installed; rollback protection cleared after complete commit")
	} else if result.Changed {
		fmt.Println("dry run only; pass --apply to install")
	} else {
		fmt.Println("state is already current; no files changed")
	}
	return nil
}
