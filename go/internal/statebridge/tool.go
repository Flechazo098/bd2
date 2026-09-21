package statebridge

import (
	"errors"
	"os"
	"path/filepath"
)

// ResolveTool prefers an explicit development override. Release packages place
// bd2-state.exe beside bd2server.exe so both components remain inspectable.
func ResolveTool(explicit string) (string, func(), error) {
	if explicit != "" {
		return filepath.Clean(explicit), func() {}, nil
	}
	if env := os.Getenv("BD2_STATE_TOOL"); env != "" {
		return filepath.Clean(env), func() {}, nil
	}
	if executable, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(executable), "bd2-state.exe")
		if info, statErr := os.Stat(sibling); statErr == nil && !info.IsDir() {
			return sibling, func() {}, nil
		}
	}
	return "", func() {}, errors.New("statebridge: bd2-state.exe is missing beside bd2server.exe; use --state-tool/--tool for a development build")
}
