// Package stateio defines the storage boundary for domain state snapshots.
package stateio

import (
	"fmt"
	"sync"
)

func RequireNoEntries(store EntryStore, domain string, buckets ...string) error {
	for _, bucket := range buckets {
		entries, err := store.ListEntries(domain, bucket)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return fmt.Errorf("%s.%s entries exist without core", domain, bucket)
		}
	}
	return nil
}

// Store loads and saves opaque domain payloads. A missing name returns nil,
// nil; Save must install a complete payload before returning successfully.
type Store interface {
	Load(name string) ([]byte, error)
	Save(name string, payload []byte) error
}

// EntryStore holds independently updated values owned by a domain. Calls made
// within a request operation participate in the same transaction as Store.
type EntryStore interface {
	LoadEntry(domain, bucket, key string) ([]byte, bool, error)
	ListEntries(domain, bucket string) (map[string][]byte, error)
	PutEntry(domain, bucket, key string, payload []byte) error
	DeleteEntry(domain, bucket, key string) (bool, error)
}

type EntryMutation struct {
	Bucket  string
	Key     string
	Payload []byte
	Delete  bool
}

// AtomicEntryStore applies a domain core and its entry changes together.
// A nil core leaves the existing core unchanged.
type AtomicEntryStore interface {
	Store
	EntryStore
	SaveWithEntries(domain string, core []byte, changes []EntryMutation) error
}

type RequestOperation interface {
	Commit() error
	Rollback() error
}

type TransactionalStore interface {
	Store
	Check() error
	BeginOperation() (RequestOperation, error)
	Close() error
}

// Memory is a detached in-memory Store, useful for tests and ephemeral state.
type Memory struct {
	mu      sync.Mutex
	data    map[string][]byte
	entries map[string]map[string]map[string][]byte
}

func NewMemory() *Memory {
	return &Memory{data: make(map[string][]byte), entries: make(map[string]map[string]map[string][]byte)}
}

func (m *Memory) Load(name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.data[name]...), nil
}

func (m *Memory) Save(name string, payload []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[name] = append([]byte(nil), payload...)
	return nil
}

func (m *Memory) LoadEntry(domain, bucket, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	payload, ok := m.entries[domain][bucket][key]
	return append([]byte(nil), payload...), ok, nil
}

func (m *Memory) ListEntries(domain, bucket string) (map[string][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string][]byte, len(m.entries[domain][bucket]))
	for key, payload := range m.entries[domain][bucket] {
		out[key] = append([]byte(nil), payload...)
	}
	return out, nil
}

func (m *Memory) PutEntry(domain, bucket, key string, payload []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries[domain] == nil {
		m.entries[domain] = make(map[string]map[string][]byte)
	}
	if m.entries[domain][bucket] == nil {
		m.entries[domain][bucket] = make(map[string][]byte)
	}
	m.entries[domain][bucket][key] = append([]byte(nil), payload...)
	return nil
}

func (m *Memory) DeleteEntry(domain, bucket, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.entries[domain][bucket][key]
	delete(m.entries[domain][bucket], key)
	return ok, nil
}

func (m *Memory) SaveWithEntries(domain string, core []byte, changes []EntryMutation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if core != nil {
		m.data[domain] = append([]byte(nil), core...)
	}
	if m.entries[domain] == nil {
		m.entries[domain] = make(map[string]map[string][]byte)
	}
	for _, change := range changes {
		if m.entries[domain][change.Bucket] == nil {
			m.entries[domain][change.Bucket] = make(map[string][]byte)
		}
		if change.Delete {
			delete(m.entries[domain][change.Bucket], change.Key)
		} else {
			m.entries[domain][change.Bucket][change.Key] = append([]byte(nil), change.Payload...)
		}
	}
	return nil
}
