package player

import (
	"path/filepath"
	"sync"

	"bd2server/internal/stateio"
)

var testStores sync.Map

func testStore(path string) stateio.Store {
	dir := filepath.Dir(path)
	store, _ := testStores.LoadOrStore(dir, stateio.NewMemory())
	return store.(stateio.Store)
}
