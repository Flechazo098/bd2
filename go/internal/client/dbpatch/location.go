package dbpatch

import (
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	clientlayout "bd2server/internal/client/layout"
)

// LocalDatabasePath resolves the native or isolated instance database. A patch
// cannot provide a filename or redirect this location.
func LocalDatabasePath(game string) (string, error) {
	installation, err := clientlayout.Resolve(game)
	if err != nil {
		return "", err
	}
	container := installation.Root
	if installation.Kind == clientlayout.MacOS {
		container = filepath.Dir(container)
	}
	parent := filepath.Dir(container)
	marker, err := os.ReadFile(filepath.Join(parent, "instance.json"))
	var persistent string
	if err == nil {
		var manifest struct {
			Schema int    `json:"schema_version"`
			ID     string `json:"id"`
		}
		if err := json.Unmarshal(marker, &manifest); err != nil || manifest.Schema != 1 || manifest.ID != filepath.Base(parent) || filepath.Base(container) != "game" {
			return "", errors.New("dbpatch: invalid client instance manifest")
		}
		persistent = filepath.Join(parent, "unity", "persistent")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		switch runtime.GOOS {
		case "windows":
			persistent = filepath.Join(home, "AppData", "LocalLow", "Gamfs", "BrownDust II")
		case "darwin":
			persistent = filepath.Join(home, "Library", "Application Support", "Gamfs", "BrownDust II")
		default:
			return "", fmt.Errorf("dbpatch: unsupported platform %s", runtime.GOOS)
		}
	}
	return filepath.Join(persistent, "Data", fmt.Sprintf("%X", sha1.Sum([]byte("ClientLocal_v1")))), nil
}
