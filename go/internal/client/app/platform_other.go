//go:build !windows && !darwin

package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

func ShowFatalError(err error) {
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "BD2 Client Studio:", err)
	}
}

func openBrowser(url string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", url).Start()
	}
	return exec.Command("xdg-open", url).Start()
}

func browseForGameDirectory(string) (string, error) {
	return "", errors.New("the native directory picker is unavailable on this platform; enter the Windows client directory manually")
}

func browseForResourceDirectory(string) (string, error) {
	return "", errors.New("the native directory picker is unavailable on this platform; enter the resource directory manually")
}

func launchGame(string) error {
	return errors.New("the Brown Dust II client is not supported on Linux")
}
