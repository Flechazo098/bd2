//go:build !windows && !darwin

package app

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
)

func ShowFatalError(err error) {
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "BD2 Launcher:", err)
	}
}

func launchGame(string, string, *slog.Logger) error {
	return errors.New("the Brown Dust II client is not supported on Linux")
}
