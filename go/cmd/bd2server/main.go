package main

import (
	"bd2server/internal/server/app"
	"bd2server/internal/server/platform/updates"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

func main() {
	if err := app.ConfigureLogging(os.Stderr, "", ""); err != nil {
		fmt.Fprintln(os.Stderr, "logging configuration failed:", err)
		os.Exit(2)
	}
	if handled, err := runDevelopmentCommand(os.Args[1:]); handled {
		if err != nil {
			slog.Error("development command failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = app.Serve(os.Args[2:])
	case "state":
		err = app.State(os.Args[2:])
	case "resources":
		err = app.Resources(os.Args[2:])
	case "preflight":
		err = app.Preflight(os.Args[2:])
	case "update":
		err = runUpdate(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	var restart *updates.RestartError
	if errors.As(err, &restart) {
		if err := restart.Plan.Execute(time.Now()); err != nil {
			slog.Error("update switch failed; the current version stays installed", "error", err)
			os.Exit(1)
		}
		os.Exit(updates.ExitRestart)
	}
	if err != nil {
		slog.Error("command failed", "command", os.Args[1], "error", err)
		var exit *updates.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `bd2server - BrownDust II server

Usage:
	bd2server preflight [--data-dir DIR] [--version-config FILE]
	bd2server serve [--data-dir DIR] [--version-config FILE] [options]
	bd2server resources fetch --output DIR [--version-config FILE]
	bd2server state check [options]
	bd2server update status|check|apply|cancel [--component server|schedules] [--data-dir DIR] [--in 10m | --now] [--confirm-irreversible] [--json]

The server binds to loopback by default.
Logging: --log-level trace|debug|info|warn|error; --log-color auto|always|never.
BD2_LOG_LEVEL and BD2_LOG_COLOR set defaults for all commands.`+developmentUsage())
}
