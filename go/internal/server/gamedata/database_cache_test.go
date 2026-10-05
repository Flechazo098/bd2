package gamedata

import (
	"archive/zip"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func cachePlain(t *testing.T, value int) []byte {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(fmt.Sprintf("CREATE TABLE test(value INTEGER);INSERT INTO test VALUES(%d)", value)); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var raw []byte
	err = conn.Raw(func(v any) error {
		var e error
		raw, e = v.(interface{ Serialize() ([]byte, error) }).Serialize()
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func cacheArchive(t *testing.T, root string, marker byte) {
	t.Helper()
	dir := filepath.Join(root, "v1", "release")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ArchiveName)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for _, logical := range []string{"common", "pack1", "pack2"} {
		name, _ := DatabaseName(logical)
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write([]byte{marker}); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestDatabaseCacheSingleFlightReadOnlyAndLifetime(t *testing.T) {
	root := t.TempDir()
	cacheArchive(t, root, 1)
	plain := cachePlain(t, 7)
	c := NewDatabaseCache(1 << 20)
	defer c.Close()
	var loads atomic.Int32
	c.loader = func(string, string, string) ([]byte, error) { loads.Add(1); return plain, nil }
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := c.WithDatabase(root, "v1", "common", func(db *sql.DB) error {
				var n int
				if err := db.QueryRow("SELECT value FROM test").Scan(&n); err != nil {
					return err
				}
				if n != 7 {
					return fmt.Errorf("wrong value")
				}
				if _, err := db.Exec("INSERT INTO test VALUES(8)"); err == nil {
					return fmt.Errorf("write accepted")
				}
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if loads.Load() != 1 {
		t.Fatal("duplicate decrypt", loads.Load())
	}
	db, release, err := c.Open(root, "v1", "common")
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = db.QueryRow("SELECT value FROM test").Scan(&n); err != nil {
		t.Fatal("active lease closed", err)
	}
	release()
	release()
	if err = db.Ping(); err == nil {
		t.Fatal("released retired database alive")
	}
}
func TestDatabaseCacheFailureRetryReplacementAndEviction(t *testing.T) {
	root := t.TempDir()
	cacheArchive(t, root, 1)
	plain := cachePlain(t, 1)
	c := NewDatabaseCache(int64(len(plain)))
	defer c.Close()
	var loads int
	c.loader = func(string, string, string) ([]byte, error) {
		loads++
		if loads == 1 {
			return nil, fmt.Errorf("failure")
		}
		return plain, nil
	}
	if _, _, err := c.Open(root, "v1", "common"); err == nil {
		t.Fatal("failure missing")
	}
	first, release, err := c.Open(root, "v1", "common")
	if err != nil {
		t.Fatal(err)
	}
	release()
	second, done, err := c.Open(filepath.Join(root, "."), "v1", "common")
	if err != nil || first != second || loads != 2 {
		t.Fatal("canonical hit failed", err, loads)
	}
	done()
	_, done, err = c.Open(root, "v1", "pack1")
	if err != nil {
		t.Fatal(err)
	}
	done()
	if err = first.Ping(); err == nil {
		t.Fatal("LRU not evicted")
	}
	old, oldRelease, err := c.Open(root, "v1", "pack1")
	if err != nil {
		t.Fatal(err)
	}
	cacheArchive(t, root, 2)
	plain = cachePlain(t, 9)
	current, currentRelease, err := c.Open(root, "v1", "pack1")
	if err != nil {
		t.Fatal(err)
	}
	if old == current {
		t.Fatal("replacement reused stale image")
	}
	var n int
	if err = current.QueryRow("SELECT value FROM test").Scan(&n); err != nil || n != 9 {
		t.Fatal("replacement wrong", n, err)
	}
	oldRelease()
	currentRelease()
}

func TestDatabaseCacheMutationDuringLoadFailsAndDoesNotPoison(t *testing.T) {
	root := t.TempDir()
	cacheArchive(t, root, 1)
	plain := cachePlain(t, 3)
	c := NewDatabaseCache(1 << 20)
	defer c.Close()
	calls := 0
	c.loader = func(string, string, string) ([]byte, error) {
		calls++
		if calls == 1 {
			cacheArchive(t, root, 2)
		}
		return plain, nil
	}
	if _, _, err := c.Open(root, "v1", "common"); err == nil {
		t.Fatal("replaced archive was cached")
	}
	db, release, err := c.Open(root, "v1", "common")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var n int
	if err = db.QueryRow("SELECT value FROM test").Scan(&n); err != nil || n != 3 || calls != 2 {
		t.Fatal(n, calls, err)
	}
}

func TestSharedDatabaseCacheCloseStartsFreshGeneration(t *testing.T) {
	root := t.TempDir()
	cacheArchive(t, root, 1)
	plain := cachePlain(t, 11)
	sharedDatabaseCacheMu.Lock()
	previous := sharedDatabaseCache
	current := NewDatabaseCache(1 << 20)
	current.loader = func(string, string, string) ([]byte, error) { return plain, nil }
	sharedDatabaseCache = current
	sharedDatabaseCacheMu.Unlock()
	t.Cleanup(func() {
		_ = CloseDatabaseCache()
		sharedDatabaseCacheMu.Lock()
		empty := sharedDatabaseCache
		sharedDatabaseCache = previous
		sharedDatabaseCacheMu.Unlock()
		_ = empty.Close()
	})
	old, releaseOld, err := OpenDatabase(root, "v1", "common")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseOld()
	if err = CloseDatabaseCache(); err != nil {
		t.Fatal(err)
	}
	sharedDatabaseCacheMu.Lock()
	fresh := sharedDatabaseCache
	fresh.loader = func(string, string, string) ([]byte, error) { return plain, nil }
	sharedDatabaseCacheMu.Unlock()
	if fresh == current {
		t.Fatal("global generation not replaced")
	}
	var n int
	if err = old.QueryRow("SELECT value FROM test").Scan(&n); err != nil || n != 11 {
		t.Fatal("retired active lease unavailable", n, err)
	}
	newDB, releaseNew, err := OpenDatabase(root, "v1", "common")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseNew()
	if newDB == old {
		t.Fatal("new generation reused old handle")
	}
	if err = newDB.QueryRow("SELECT value FROM test").Scan(&n); err != nil || n != 11 {
		t.Fatal(n, err)
	}
	releaseOld()
	if err = old.Ping(); err == nil {
		t.Fatal("retired released generation retained database")
	}
}

func TestDatabaseCacheRootAndVersionIsolation(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	cacheArchive(t, rootA, 1)
	cacheArchive(t, rootB, 1)
	archive, err := os.ReadFile(filepath.Join(rootA, "v1", "release", ArchiveName))
	if err != nil {
		t.Fatal(err)
	}
	v2dir := filepath.Join(rootA, "v2", "release")
	if err = os.MkdirAll(v2dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(v2dir, ArchiveName), archive, 0600); err != nil {
		t.Fatal(err)
	}
	plainA, plainB, plainV2 := cachePlain(t, 1), cachePlain(t, 2), cachePlain(t, 3)
	c := NewDatabaseCache(1 << 20)
	defer c.Close()
	loads := 0
	c.loader = func(root, version, _ string) ([]byte, error) {
		loads++
		if version == "v2" {
			return plainV2, nil
		}
		if root == rootB {
			return plainB, nil
		}
		return plainA, nil
	}
	type item struct {
		root, version string
		want          int
	}
	handles := map[*sql.DB]bool{}
	for _, input := range []item{{rootA, "v1", 1}, {rootB, "v1", 2}, {rootA, "v2", 3}} {
		db, release, err := c.Open(input.root, input.version, "common")
		if err != nil {
			t.Fatal(err)
		}
		var value int
		if err = db.QueryRow("SELECT value FROM test").Scan(&value); err != nil || value != input.want {
			release()
			t.Fatal("root/version crossed", value, input.want, err)
		}
		if handles[db] {
			release()
			t.Fatal("isolated inputs shared handle")
		}
		handles[db] = true
		release()
	}
	if loads != 3 {
		t.Fatal("isolated inputs did not load separately", loads)
	}
	for _, input := range []item{{rootA, "v1", 1}, {rootB, "v1", 2}, {rootA, "v2", 3}} {
		if err = c.WithDatabase(input.root, input.version, "common", func(db *sql.DB) error { return db.Ping() }); err != nil {
			t.Fatal(err)
		}
	}
	if loads != 3 {
		t.Fatal("isolated caches did not hit", loads)
	}
}
