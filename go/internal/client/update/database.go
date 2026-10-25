package update

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"

	"bd2server/internal/client/dbpatch"
)

// PrepareLocalDatabase is shared by the normal launcher and the independent
// development instance entry point. It never starts the game itself.
func (m *Manager) PrepareLocalDatabase(ctx context.Context, client *http.Client, game, origin string) error {
	if client == nil {
		client = m.options.Client
	}
	runtime, err := m.Runtime(game)
	if err != nil {
		return err
	}
	_, err = dbpatch.Run(ctx, dbpatch.Options{
		GameDirectory: game, Origin: origin, GameVersion: runtime.GameVersion,
		Client: client, Logger: m.options.Logger, Running: func() (bool, error) { return m.localDatabaseRunning(game) },
	})
	return err
}

func (m *Manager) localDatabaseRunning(game string) (bool, error) {
	target, err := dbpatch.LocalDatabasePath(game)
	if err != nil {
		return false, err
	}
	processes, err := runningExecutables()
	if err != nil {
		return false, err
	}
	for executable := range processes {
		name := filepath.Base(executable)
		if !strings.EqualFold(name, "BrownDust II.exe") && name != "BrownDust II" {
			continue
		}
		directory := filepath.Dir(executable)
		if name == "BrownDust II" {
			directory = filepath.Dir(filepath.Dir(directory))
		}
		path, err := dbpatch.LocalDatabasePath(directory)
		if err != nil {
			return false, err
		}
		if executableKey(target) == executableKey(path) {
			return true, nil
		}
	}
	return false, nil
}
