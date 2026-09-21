package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	controlv1 "bd2server/gen/state/control/v1"
	"bd2server/internal/gacha"
	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/statebridge"
)

func repairStateBeforeServe(stateDir, toolOverride, gameDataVersion string, catalog *gamedata.RegularGachaCatalog, starter *player.Starter) error {
	if recovered, err := statebridge.RecoverInterruptedInstall(stateDir); err != nil {
		return fmt.Errorf("recover interrupted state repair: %w", err)
	} else if recovered {
		slog.Warn("restored state after interrupted repair transaction")
	}
	complete, err := statebridge.CompleteStateAvailable(stateDir)
	if err != nil {
		return err
	}
	if !complete {
		slog.Info("state repair deferred until all state domains exist")
		return nil
	}
	tool, cleanup, err := statebridge.ResolveTool(toolOverride)
	if err != nil {
		return err
	}
	defer cleanup()
	stepGroup, stepIDs := gacha.StepUpMigrationFact()
	validation, err := statebridge.BuildValidationContext(gameDataVersion, catalog, starter, stepGroup, stepIDs)
	if err != nil {
		return err
	}
	result, err := statebridge.Repair(context.Background(), stateDir, tool, validation)
	if err != nil {
		return err
	}
	if result.Target == nil {
		return violationsError("state repair rejected", result.Violations)
	}
	if result.Changed {
		if err := statebridge.InstallRepair(stateDir, result); err != nil {
			return fmt.Errorf("install repaired state: %w", err)
		}
		slog.Info("installed Haskell state repair", "targetSHA256", fmt.Sprintf("%x", result.TargetSHA256))
	}
	verified, err := statebridge.Repair(context.Background(), stateDir, tool, validation)
	if err != nil {
		return fmt.Errorf("verify installed repair: %w", err)
	}
	if verified.Target == nil {
		return violationsError("installed state rejected", verified.Violations)
	}
	if verified.Changed {
		return errors.New("state repair is not idempotent; refusing to start")
	}
	return nil
}

func violationsError(prefix string, violations []*controlv1.Violation) error {
	parts := make([]string, 0, len(violations))
	for _, violation := range violations {
		parts = append(parts, violation.Code+": "+violation.Message)
	}
	return fmt.Errorf("%s: %s", prefix, strings.Join(parts, "; "))
}
