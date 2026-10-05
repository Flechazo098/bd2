package gamedata

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

const DefaultDatabaseCacheBytes int64 = 512 << 20

type databaseCacheKey struct {
	Path, Version, Logical string
	Size, Modified         int64
}
type databaseCacheEntry struct {
	db      *sql.DB
	size    int64
	refs    int
	used    uint64
	ready   chan struct{}
	err     error
	retired bool
}

// DatabaseCache owns read-only SQLite connections. A lease must be released
// after closing all rows; callers must never Close the returned shared DB.
// The budget bounds retained SQLite images, excluding transient decrypt buffers
// and SQLite page-cache overhead. Active leases may temporarily exceed it.
type DatabaseCache struct {
	mu            sync.Mutex
	loadMu        sync.Mutex
	entries       map[databaseCacheKey]*databaseCacheEntry
	budget, bytes int64
	clock         uint64
	closed        bool
	loader        func(string, string, string) ([]byte, error)
}

func NewDatabaseCache(maxBytes int64) *DatabaseCache {
	if maxBytes <= 0 {
		maxBytes = DefaultDatabaseCacheBytes
	}
	return &DatabaseCache{budget: maxBytes, entries: map[databaseCacheKey]*databaseCacheEntry{}, loader: ReadDatabase}
}

var sharedDatabaseCache = NewDatabaseCache(DefaultDatabaseCacheBytes)
var sharedDatabaseCacheMu sync.RWMutex
var databaseMemoryID atomic.Uint64

func databaseKey(root, version, logical string) (databaseCacheKey, error) {
	if _, err := DatabaseName(logical); err != nil {
		return databaseCacheKey{}, err
	}
	if version == "" || version == "." || version == ".." || strings.ContainsAny(version, `/\`) {
		return databaseCacheKey{}, fmt.Errorf("gamedata: invalid database version")
	}
	abs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return databaseCacheKey{}, err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return databaseCacheKey{}, err
	}
	archive := filepath.Join(abs, version, "release", ArchiveName)
	stat, err := os.Stat(archive)
	if err != nil {
		return databaseCacheKey{}, err
	}
	path := archive
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return databaseCacheKey{Path: path, Version: version, Logical: logical, Size: stat.Size(), Modified: stat.ModTime().UnixNano()}, nil
}

func memoryDatabase(plain []byte) (*sql.DB, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:bd2-gamedata-%d?mode=memory&cache=private", databaseMemoryID.Add(1)))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		db.Close()
		return nil, err
	}
	err = conn.Raw(func(driver any) error {
		d, ok := driver.(interface{ Deserialize([]byte) error })
		if !ok {
			return fmt.Errorf("gamedata: SQLite driver lacks Deserialize")
		}
		return d.Deserialize(plain)
	})
	if err == nil {
		_, err = conn.ExecContext(context.Background(), "PRAGMA query_only=ON")
	}
	if err == nil {
		_, err = conn.ExecContext(context.Background(), "PRAGMA cache_size=-4096")
	}
	if err == nil {
		var n int
		err = conn.QueryRowContext(context.Background(), "SELECT count(*) FROM sqlite_schema").Scan(&n)
	}
	closeErr := conn.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (c *DatabaseCache) Open(root, version, logical string) (*sql.DB, func(), error) {
	key, err := databaseKey(root, version, logical)
	if err != nil {
		return nil, nil, err
	}
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil, nil, fmt.Errorf("gamedata: database cache closed")
		}
		if e, ok := c.entries[key]; ok {
			if e.ready != nil {
				ready := e.ready
				c.mu.Unlock()
				<-ready
				if e.err != nil {
					return nil, nil, e.err
				}
				continue
			}
			e.refs++
			c.clock++
			e.used = c.clock
			c.mu.Unlock()
			return e.db, c.release(e), nil
		}
		e := &databaseCacheEntry{ready: make(chan struct{})}
		c.entries[key] = e
		c.mu.Unlock()
		// Serial loading limits peak encrypted + plaintext + SQLite image memory.
		c.loadMu.Lock()
		loadStarted := time.Now()
		plain, loadErr := c.loader(root, version, logical)
		var db *sql.DB
		size := int64(len(plain))
		if loadErr == nil {
			current, e2 := databaseKey(root, version, logical)
			if e2 != nil {
				loadErr = e2
			} else if current != key {
				loadErr = fmt.Errorf("gamedata: archive changed during database load")
			} else {
				db, loadErr = memoryDatabase(plain)
			}
		}
		plain = nil
		c.loadMu.Unlock()
		if loadErr == nil {
			slog.Debug("GameData database cached", "logical", logical, "version", version, "image_bytes", size, "load_ms", float64(time.Since(loadStarted).Microseconds())/1000)
		}
		c.mu.Lock()
		e.err = loadErr
		ready := e.ready
		e.ready = nil
		if loadErr != nil || c.closed {
			delete(c.entries, key)
			if db != nil {
				_ = db.Close()
			}
			if loadErr == nil {
				e.err = fmt.Errorf("gamedata: database cache closed")
			}
			close(ready)
			c.mu.Unlock()
			return nil, nil, e.err
		}
		e.db = db
		e.size = size
		e.refs = 1
		c.clock++
		e.used = c.clock
		c.bytes += size
		// Retire earlier versions of a replaced archive. Existing leases remain
		// valid until released, but cannot become a new request's database.
		for oldKey, old := range c.entries {
			if oldKey != key && oldKey.Path == key.Path && oldKey.Logical == key.Logical && old.ready == nil {
				delete(c.entries, oldKey)
				old.retired = true
				if old.refs == 0 {
					c.bytes -= old.size
					_ = old.db.Close()
				}
			}
		}
		c.evictLocked()
		close(ready)
		c.mu.Unlock()
		return db, c.release(e), nil
	}
}
func (c *DatabaseCache) release(e *databaseCacheEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			e.refs--
			if e.refs == 0 && e.retired {
				c.bytes -= e.size
				_ = e.db.Close()
			}
			c.evictLocked()
		})
	}
}
func (c *DatabaseCache) evictLocked() {
	for c.bytes > c.budget {
		var key databaseCacheKey
		var oldest *databaseCacheEntry
		for k, e := range c.entries {
			if e.ready == nil && e.refs == 0 && (oldest == nil || e.used < oldest.used) {
				key = k
				oldest = e
			}
		}
		if oldest == nil {
			return
		}
		delete(c.entries, key)
		c.bytes -= oldest.size
		oldest.retired = true
		_ = oldest.db.Close()
	}
}

// Close retires all images; active queries finish before their final release.
func (c *DatabaseCache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	var first error
	for key, e := range c.entries {
		if e.ready != nil {
			continue
		}
		delete(c.entries, key)
		e.retired = true
		if e.refs == 0 {
			c.bytes -= e.size
			if err := e.db.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}
func (c *DatabaseCache) WithDatabase(root, version, logical string, query func(*sql.DB) error) error {
	if query == nil {
		return fmt.Errorf("gamedata: missing query callback")
	}
	db, release, err := c.Open(root, version, logical)
	if err != nil {
		return err
	}
	defer release()
	return query(db)
}
func OpenDatabase(root, version, logical string) (*sql.DB, func(), error) {
	sharedDatabaseCacheMu.RLock()
	defer sharedDatabaseCacheMu.RUnlock()
	return sharedDatabaseCache.Open(root, version, logical)
}
func WithDatabase(root, version, logical string, query func(*sql.DB) error) error {
	if query == nil {
		return fmt.Errorf("gamedata: missing query callback")
	}
	db, release, err := OpenDatabase(root, version, logical)
	if err != nil {
		return err
	}
	defer release()
	return query(db)
}

// CloseDatabaseCache retires the current generation. Active leases finish
// normally; the next OpenDatabase starts with a fresh empty cache.
func CloseDatabaseCache() error {
	sharedDatabaseCacheMu.Lock()
	old := sharedDatabaseCache
	sharedDatabaseCache = NewDatabaseCache(DefaultDatabaseCacheBytes)
	sharedDatabaseCacheMu.Unlock()
	return old.Close()
}
