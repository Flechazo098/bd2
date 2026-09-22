package statetx

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const requestFormat = 3

type undoRecord struct {
	Name   string `json:"name"`
	Exists bool   `json:"exists"`
	SHA256 string `json:"sha256,omitempty"`
}

type undoManifest struct {
	Format  int          `json:"format"`
	Records []undoRecord `json:"records"`
}

type finalRecord struct {
	Name   string `json:"name"`
	Exists bool   `json:"exists"`
	SHA256 string `json:"sha256,omitempty"`
}

type finalManifest struct {
	Format  int           `json:"format"`
	Records []finalRecord `json:"records"`
}

// Operation is a write-ahead transaction around one authenticated request.
// Begin keeps the coordinator locked until Commit or Rollback. Domain stores
// remain responsible for typed validation and their normal single-file
// atomic writes; the durable undo log makes the complete request all-or-none
// across the account's state files.
type Operation struct {
	coordinator *Coordinator
	journal     string
	undo        undoManifest
	done        bool
}

type RequestOperation interface {
	Commit() error
	Rollback() error
}

func (s *Coordinator) BeginOperation() (RequestOperation, error) {
	return s.Begin()
}

// Begin persists a complete undo generation before domain code is allowed to
// mutate state. Missing state files are represented explicitly so first-run
// accounts can be rolled back without inventing an empty JSON document.
func (s *Coordinator) Begin() (*Operation, error) {
	s.mu.Lock()
	unlock := true
	defer func() {
		if unlock {
			s.mu.Unlock()
		}
	}()
	if s.failed {
		return nil, errors.New("statetx: previous transaction failed; restart required")
	}
	journal := filepath.Join(s.root, journalName)
	preparing := filepath.Join(s.root, journalName+".preparing")
	if _, err := os.Lstat(journal); err == nil {
		s.failed = true
		return nil, errors.New("statetx: existing journal; restart required")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(preparing); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("statetx: preparing path is not a directory")
		}
		if err := os.RemoveAll(preparing); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.Mkdir(preparing, 0o700); err != nil {
		return nil, err
	}
	prepared := false
	defer func() {
		if !prepared {
			_ = os.RemoveAll(preparing)
		}
	}()
	if err := os.Mkdir(filepath.Join(preparing, "before"), 0o700); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(s.allowed))
	for name := range s.allowed {
		names = append(names, name)
	}
	sort.Strings(names)
	undo := undoManifest{Format: requestFormat, Records: make([]undoRecord, 0, len(names))}
	for _, name := range names {
		info, exists, err := s.statOptionalTarget(name)
		if err != nil {
			return nil, err
		}
		record := undoRecord{Name: name, Exists: exists}
		if exists {
			if cached, ok := s.cache[name]; ok && sameFileGeneration(cached.info, info) {
				record.SHA256 = cached.digest
			} else {
				data, err := os.ReadFile(filepath.Join(s.root, name))
				if err != nil {
					return nil, err
				}
				record.SHA256 = digest(data)
			}
			s.cache[name] = fileDigest{info: info, digest: record.SHA256}
			if err := snapshotBefore(filepath.Join(s.root, name), filepath.Join(preparing, "before", name)); err != nil {
				return nil, err
			}
		} else {
			delete(s.cache, name)
		}
		undo.Records = append(undo.Records, record)
	}
	encoded, err := json.Marshal(undo)
	if err != nil {
		return nil, err
	}
	if err := syncDir(filepath.Join(preparing, "before")); err != nil {
		return nil, err
	}
	if err := writeAtomicSynced(filepath.Join(preparing, "prepared.json"), encoded); err != nil {
		return nil, err
	}
	if err := syncDir(preparing); err != nil {
		return nil, err
	}
	if err := replaceFile(preparing, journal); err != nil {
		return nil, err
	}
	if err := syncDir(s.root); err != nil {
		return nil, err
	}
	prepared = true
	unlock = false
	return &Operation{coordinator: s, journal: journal, undo: undo}, nil
}

// Commit records and validates the exact final generation before making it
// authoritative. Response encoding must be complete before this is called.
func (o *Operation) Commit() error {
	if o == nil || o.coordinator == nil {
		return errors.New("statetx: nil request transaction")
	}
	if o.done {
		return errors.New("statetx: request transaction already finished")
	}
	s := o.coordinator
	defer s.mu.Unlock()
	o.done = true
	final := finalManifest{Format: requestFormat, Records: make([]finalRecord, 0, len(o.undo.Records))}
	finalCache := make(map[string]fileDigest, len(o.undo.Records))
	for _, entry := range o.undo.Records {
		info, exists, err := s.statOptionalTarget(entry.Name)
		if err != nil {
			return o.failAndRollback(fmt.Errorf("statetx: read final %s: %w", entry.Name, err))
		}
		record := finalRecord{Name: entry.Name, Exists: exists}
		if exists {
			if cached, ok := s.cache[entry.Name]; ok && sameFileGeneration(cached.info, info) {
				record.SHA256 = cached.digest
			} else {
				data, err := os.ReadFile(filepath.Join(s.root, entry.Name))
				if err != nil {
					return o.failAndRollback(fmt.Errorf("statetx: read final %s: %w", entry.Name, err))
				}
				record.SHA256 = digest(data)
			}
			finalCache[entry.Name] = fileDigest{info: info, digest: record.SHA256}
		}
		final.Records = append(final.Records, record)
	}
	encoded, err := json.Marshal(final)
	if err != nil {
		return o.failAndRollback(err)
	}
	if err := writeAtomicSynced(filepath.Join(o.journal, "committed.json"), encoded); err != nil {
		return o.failAndRollback(fmt.Errorf("statetx: write commit marker: %w", err))
	}
	if err := os.RemoveAll(o.journal); err != nil {
		// committed is authoritative. Keep serving the successful response but
		// reject every later request until startup validates and cleans it.
		s.failed = true
		return nil
	}
	s.cache = finalCache
	return nil
}

// Rollback is used when domain execution or response encoding fails. If no
// file changed, the request can safely fail without poisoning the process. If
// typed stores already published any change in memory, disk is restored but
// the coordinator enters fail-stop until restart reloads all stores.
func (o *Operation) Rollback() error {
	if o == nil || o.coordinator == nil {
		return errors.New("statetx: nil request transaction")
	}
	if o.done {
		return nil
	}
	s := o.coordinator
	defer s.mu.Unlock()
	o.done = true
	changed, err := s.undoChanged(o.undo)
	if err != nil {
		s.failed = true
		return err
	}
	if changed {
		if err := s.restoreUndo(o.journal, o.undo); err != nil {
			s.failed = true
			return err
		}
		s.failed = true
		return errors.New("statetx: request rolled back after state mutation; restart required")
	}
	if err := os.RemoveAll(o.journal); err != nil {
		s.failed = true
		return err
	}
	return nil
}

// Abort closes an operation for tests that simulate a process crash. It does
// not touch the journal or live files; the next Open performs recovery.
func (o *Operation) Abort() {
	if o == nil || o.coordinator == nil || o.done {
		return
	}
	o.done = true
	o.coordinator.mu.Unlock()
}

func (o *Operation) failAndRollback(cause error) error {
	s := o.coordinator
	changed, inspectErr := s.undoChanged(o.undo)
	if inspectErr != nil {
		s.failed = true
		return fmt.Errorf("%w; inspect rollback: %v", cause, inspectErr)
	}
	if changed {
		if rollbackErr := s.restoreUndo(o.journal, o.undo); rollbackErr != nil {
			s.failed = true
			return fmt.Errorf("%w; rollback failed: %v", cause, rollbackErr)
		}
		s.failed = true
		return fmt.Errorf("%w; state restored but restart required", cause)
	}
	if cleanupErr := os.RemoveAll(o.journal); cleanupErr != nil {
		s.failed = true
		return fmt.Errorf("%w; cleanup failed: %v", cause, cleanupErr)
	}
	return cause
}

func (s *Coordinator) readOptionalTarget(name string) ([]byte, bool, error) {
	info, exists, err := s.statOptionalTarget(name)
	if err != nil || !exists {
		return nil, exists, err
	}
	data, err := os.ReadFile(filepath.Join(s.root, name))
	if err != nil {
		return nil, false, err
	}
	_ = info
	return data, true, nil
}

func (s *Coordinator) statOptionalTarget(name string) (os.FileInfo, bool, error) {
	if !s.allowed[name] {
		return nil, false, fmt.Errorf("statetx: unregistered state file %q", name)
	}
	path := filepath.Join(s.root, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("statetx: %s is not a regular state file", name)
	}
	return info, true, nil
}

func sameFileGeneration(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) &&
		left.Size() == right.Size() && left.ModTime() == right.ModTime()
}

func (s *Coordinator) undoChanged(undo undoManifest) (bool, error) {
	for _, entry := range undo.Records {
		info, exists, err := s.statOptionalTarget(entry.Name)
		if err != nil {
			return false, err
		}
		if exists != entry.Exists {
			return true, nil
		}
		if exists {
			cached, ok := s.cache[entry.Name]
			if !ok || cached.digest != entry.SHA256 || !sameFileGeneration(cached.info, info) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Coordinator) restoreUndo(journal string, undo undoManifest) error {
	for _, entry := range undo.Records {
		target := filepath.Join(s.root, entry.Name)
		if entry.Exists {
			image := filepath.Join(journal, "before", entry.Name)
			data, err := os.ReadFile(image)
			if err != nil || digest(data) != entry.SHA256 {
				return fmt.Errorf("statetx: invalid undo image for %s", entry.Name)
			}
			if err := replaceFromImage(image, target); err != nil {
				return fmt.Errorf("statetx: restore %s: %w", entry.Name, err)
			}
			continue
		}
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("statetx: refuse to remove non-regular %s", entry.Name)
		}
		if err := os.Remove(target); err != nil {
			return fmt.Errorf("statetx: remove first-run %s: %w", entry.Name, err)
		}
	}
	if err := os.RemoveAll(journal); err != nil {
		return fmt.Errorf("statetx: remove undo journal: %w", err)
	}
	return nil
}

func (s *Coordinator) recoverRequestLocked(journal string) error {
	encoded, err := os.ReadFile(filepath.Join(journal, "prepared.json"))
	if errors.Is(err, os.ErrNotExist) {
		return os.RemoveAll(journal)
	}
	if err != nil {
		return err
	}
	var undo undoManifest
	if err := json.Unmarshal(encoded, &undo); err != nil || undo.Format != requestFormat || len(undo.Records) != len(s.allowed) {
		return errors.New("statetx: malformed request undo manifest")
	}
	seen := make(map[string]bool, len(undo.Records))
	for _, entry := range undo.Records {
		if !s.allowed[entry.Name] || seen[entry.Name] || entry.Exists && entry.SHA256 == "" || !entry.Exists && entry.SHA256 != "" {
			return errors.New("statetx: invalid request undo target")
		}
		seen[entry.Name] = true
		if entry.Exists {
			data, err := os.ReadFile(filepath.Join(journal, "before", entry.Name))
			if err != nil || digest(data) != entry.SHA256 {
				return fmt.Errorf("statetx: invalid request undo image for %s", entry.Name)
			}
		}
	}
	commitBytes, markerErr := os.ReadFile(filepath.Join(journal, "committed.json"))
	if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
		return markerErr
	}
	if errors.Is(markerErr, os.ErrNotExist) {
		return s.restoreUndo(journal, undo)
	}
	var final finalManifest
	if err := json.Unmarshal(commitBytes, &final); err != nil || final.Format != requestFormat || len(final.Records) != len(undo.Records) {
		return errors.New("statetx: malformed request commit manifest")
	}
	seen = make(map[string]bool, len(final.Records))
	for _, entry := range final.Records {
		if !s.allowed[entry.Name] || seen[entry.Name] || entry.Exists && entry.SHA256 == "" || !entry.Exists && entry.SHA256 != "" {
			return errors.New("statetx: invalid request final target")
		}
		seen[entry.Name] = true
		current, exists, err := s.readOptionalTarget(entry.Name)
		if err != nil || exists != entry.Exists || exists && digest(current) != entry.SHA256 {
			return fmt.Errorf("statetx: committed request file %s is not the final image", entry.Name)
		}
	}
	return os.RemoveAll(journal)
}
