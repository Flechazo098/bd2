//go:build !windows && !darwin

package app

import (
	"errors"
	"fmt"
	"os"
)

func ShowFatalError(err error) {
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "BD2 Launcher:", err)
	}
}

func launchGame(string, string) error {
	return errors.New("the Brown Dust II client is not supported on Linux")
}
