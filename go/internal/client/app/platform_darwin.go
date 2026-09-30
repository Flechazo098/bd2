//go:build darwin

package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func ShowFatalError(err error) {
	if err == nil {
		return
	}
	message := strings.ReplaceAll(err.Error(), `"`, `\"`)
	_ = exec.Command("osascript", "-e", `display alert "BD2 Client Studio" message "`+message+`" as critical`).Run()
}

func openBrowser(url string) error {
	return exec.Command("open", url).Start()
}

func browseForGameDirectory(language string) (string, error) {
	prompt := "Select the Brown Dust II.app bundle or its parent folder"
	if language == "zh-CN" {
		prompt = "选择 Brown Dust II.app 或其所在文件夹"
	}
	return macDirectoryPicker(prompt)
}

func browseForResourceDirectory(language string) (string, error) {
	prompt := "Select the CDN directory containing ServerData and GameData"
	if language == "zh-CN" {
		prompt = "选择包含 ServerData 和 GameData 的 CDN 目录"
	}
	return macDirectoryPicker(prompt)
}

func macDirectoryPicker(prompt string) (string, error) {
	prompt = strings.ReplaceAll(prompt, `"`, `\"`)
	command := exec.Command("osascript", "-e", `POSIX path of (choose folder with prompt "`+prompt+`")`)
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("open macOS directory picker: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func launchGame(target string) error {
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() || !strings.EqualFold(filepath.Ext(target), ".app") {
		return fmt.Errorf("invalid macOS application bundle %q", target)
	}
	if err := exec.Command("pgrep", "-x", "BrownDust II").Run(); err == nil {
		return errGameAlreadyRunning
	}
	return exec.Command("open", target).Start()
}
