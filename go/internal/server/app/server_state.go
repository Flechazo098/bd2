package app

import (
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type serverState struct {
	Version     int `json:"version"`
	StartPackID int `json:"start_pack_id"`
}

func lockServerState(directory string, configured int) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(directory, "state.db")); err == nil {
		return errors.New("single-account state layout requires explicit offline migration")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	path := filepath.Join(directory, "server.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if entries, readErr := os.ReadDir(filepath.Join(directory, "accounts")); readErr == nil && len(entries) != 0 {
			return errors.New("server policy is missing for existing player databases; explicit repair required")
		} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		raw, err = json.Marshal(serverState{Version: 1, StartPackID: configured})
		if err != nil {
			return err
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(raw)
		syncErr := file.Sync()
		closeErr := file.Close()
		return errors.Join(writeErr, syncErr, closeErr)
	}
	if err != nil {
		return err
	}
	if err := stateio.RequireExactJSONObject(raw, "version", "start_pack_id"); err != nil {
		return err
	}
	var state serverState
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	if state.Version != 1 || (state.StartPackID != 1 && state.StartPackID != 21) {
		return errors.New("invalid permanent server story policy")
	}
	if state.StartPackID != configured {
		return fmt.Errorf("story.start_pack_id %d conflicts with permanent server policy %d", configured, state.StartPackID)
	}
	return nil
}
