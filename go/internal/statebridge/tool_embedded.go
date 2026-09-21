//go:build embedded_state_tool

package statebridge

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"os"
	"path/filepath"
)

//go:embed tool/bd2-state.exe
var embeddedStateToolBytes []byte

func embeddedStateTool() (string, func(), bool, error) {
	sum := sha256.Sum256(embeddedStateToolBytes)
	dir, err := os.MkdirTemp("", "bd2-state-"+hex.EncodeToString(sum[:6])+"-")
	if err != nil {
		return "", func() {}, true, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "bd2-state.exe")
	if err := os.WriteFile(path, embeddedStateToolBytes, 0o700); err != nil {
		cleanup()
		return "", func() {}, true, err
	}
	return path, cleanup, true, nil
}
