// Package statetx provides a crash-safe write-ahead transaction around one
// account request. It knows filenames and bytes only; domain stores retain all
// JSON and gameplay semantics.
package statetx

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const journalName = ".account-transaction"

type Coordinator struct {
	mu      sync.Mutex
	root    string
	allowed map[string]bool
	failed  bool
	cache   map[string]fileDigest
}

type fileDigest struct {
	info   os.FileInfo
	digest string
}

// Open validates the account boundary and recovers an interrupted request
// before any domain store reads its state.
func Open(root string, allowed []string) (*Coordinator, error) {
	if len(allowed) == 0 {
		return nil, errors.New("statetx: no state filenames")
	}
	s := &Coordinator{root: filepath.Clean(root), allowed: make(map[string]bool, len(allowed)), cache: make(map[string]fileDigest, len(allowed))}
	for _, name := range allowed {
		if err := validName(name); err != nil || s.allowed[name] {
			return nil, fmt.Errorf("statetx: invalid or duplicate allowed filename %q", name)
		}
		s.allowed[name] = true
	}
	if err := s.recoverLocked(); err != nil {
		return nil, err
	}
	if err := s.refreshCacheLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Coordinator) refreshCacheLocked() error {
	next := make(map[string]fileDigest, len(s.allowed))
	for name := range s.allowed {
		path := filepath.Join(s.root, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("statetx: %s is not a regular state file", name)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		next[name] = fileDigest{info: info, digest: digest(data)}
	}
	s.cache = next
	return nil
}

// Check is the request dispatcher fail-stop gate. Once memory could differ
// from a disk generation restored after an error, only restart may continue.
func (s *Coordinator) Check() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return errors.New("statetx: account persistence requires restart recovery")
	}
	return nil
}

func validName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\:`) || name == journalName {
		return errors.New("filename is not a direct child")
	}
	return nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *Coordinator) recoverLocked() error {
	preparing := filepath.Join(s.root, journalName+".preparing")
	if info, err := os.Lstat(preparing); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("statetx: preparing path is not a directory")
		}
		if err := os.RemoveAll(preparing); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	journal := filepath.Join(s.root, journalName)
	info, err := os.Lstat(journal)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("statetx: journal is not a directory")
	}
	if _, err := os.Lstat(filepath.Join(journal, "prepared.json")); err == nil {
		return s.recoverRequestLocked(journal)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Begin writes undo.json and then prepared before gameplay may execute.
	// Therefore a journal without undo.json cannot have touched live state.
	return os.RemoveAll(journal)
}

func writeAtomicSynced(path string, data []byte) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".statetx-phase-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := replaceFile(name, path); err != nil {
		return err
	}
	return syncDir(dir)
}

func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func copySynced(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return writeSynced(destination, data)
}

func replaceFromImage(image, target string) error {
	data, err := os.ReadFile(image)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".account-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceFile(file.Name(), target)
}
