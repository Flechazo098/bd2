package statebridge

import (
	"errors"
	"os"
	"path/filepath"
)

// ResolveTool prefers an explicit development override. Release builds embed
// the Haskell executable and materialize it into a private temporary directory.
func ResolveTool(explicit string) (string, func(), error) {
	if explicit != "" {
		return filepath.Clean(explicit), func() {}, nil
	}
	if env := os.Getenv("BD2_STATE_TOOL"); env != "" {
		return filepath.Clean(env), func() {}, nil
	}
	if path, cleanup, ok, err := embeddedStateTool(); err != nil {
		return "", func() {}, err
	} else if ok {
		return path, cleanup, nil
	}
	if executable, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(executable), "bd2-state.exe")
		if info, statErr := os.Stat(sibling); statErr == nil && !info.IsDir() {
			return sibling, func() {}, nil
		}
	}
	return "", func() {}, errors.New("statebridge: no Haskell state tool; use --state-tool/--tool or build the embedded release")
}
