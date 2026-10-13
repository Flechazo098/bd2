package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	// ErrConflict means the file changed after the caller read it.
	ErrConflict = errors.New("configuration changed since it was read")
	// ErrReadOnly means the deployment does not allow this process to write the file.
	ErrReadOnly = errors.New("configuration file is read-only")
)

// Revision identifies file contents. It is empty for a missing file.
type Revision string

func RevisionOf(raw []byte) Revision {
	if raw == nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return Revision(hex.EncodeToString(sum[:]))
}

// Lock pins a value that the running process must not change, such as a
// choice persisted by first initialization. Editors show Reason and Replace
// rejects any document whose value at Path differs from Value.
type Lock struct {
	Path   string `json:"path"`
	Value  any    `json:"value"`
	Reason string `json:"reason"`
}

// File is one managed document on disk. Writes are atomic, validated and
// conditional on the revision the caller read, so concurrent editors and
// manual edits cannot silently overwrite each other.
type File[T any] struct {
	document Document[T]
	path     string
	loaded   Revision

	mu    sync.Mutex
	locks []Lock
}

// Snapshot is the current state of a managed file.
type Snapshot[T any] struct {
	Value    T
	Revision Revision
	// Loaded is the revision this process applied at startup.
	Loaded   Revision
	Writable bool
	// Problems is non-empty when the file was changed outside this process
	// into an invalid document. Value then holds the best-effort decoding.
	Problems Problems
	// Advice holds non-blocking warnings for a valid document.
	Advice   Problems
	Modified time.Time
}

// Pending reports whether the file differs from the revision in effect.
func (s Snapshot[T]) Pending() bool { return s.Revision != s.Loaded }

func (f *File[T]) Name() string             { return f.document.Name }
func (f *File[T]) Path() string             { return f.path }
func (f *File[T]) Document() Document[T]    { return f.document }
func (f *File[T]) LoadedRevision() Revision { return f.loaded }

// Lock pins a value for the lifetime of this process.
func (f *File[T]) Lock(lock Lock) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.locks = append(f.locks, lock)
}

func (f *File[T]) Locks() []Lock {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Lock(nil), f.locks...)
}

func (f *File[T]) Read() (Snapshot[T], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readLocked()
}

func (f *File[T]) readLocked() (Snapshot[T], error) {
	snapshot := Snapshot[T]{Loaded: f.loaded, Writable: writable(f.path)}
	info, err := os.Stat(f.path)
	if errors.Is(err, os.ErrNotExist) {
		snapshot.Value = f.document.Defaults()
		return snapshot, nil
	}
	if err != nil {
		return snapshot, err
	}
	if !info.Mode().IsRegular() {
		return snapshot, fmt.Errorf("%s configuration: %s is not a regular file", f.document.Name, f.path)
	}
	raw, err := os.ReadFile(f.path)
	if err != nil {
		return snapshot, err
	}
	snapshot.Revision, snapshot.Modified = RevisionOf(raw), info.ModTime()
	value, err := f.document.Parse(raw)
	if err != nil {
		snapshot.Problems = AsProblems(err)
		value, _ = Decode(raw, f.document.base(), DecodeOptions{})
	} else {
		snapshot.Advice = f.document.advise(value, f.path)
	}
	snapshot.Value = value
	return snapshot, nil
}

// Check validates value against the document and the process locks.
func (f *File[T]) Check(value T) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkLocked(value)
}

func (f *File[T]) checkLocked(value T) error {
	problems := AsProblems(f.document.Validate(value))
	if len(f.locks) != 0 {
		document, err := normalize(value)
		if err != nil {
			return Problems{{Rule: RuleDecode, Message: err.Error()}}
		}
		for _, lock := range f.locks {
			found := lookup(document, lock.Path)
			if len(found) != 1 || !sameJSON(found[0].value, lock.Value) {
				problems.Add(lock.Path, RuleConflict, "is fixed to %s: %s", canonicalJSON(lock.Value), lock.Reason)
			}
		}
	}
	return problems.Err()
}

// Replace writes value when the file still has the expected revision.
func (f *File[T]) Replace(value T, expected Revision) (Snapshot[T], error) {
	return f.Update(expected, func(current *T) error {
		*current = value
		return nil
	})
}

// Update applies change to the current value and writes the result when the
// file still has the expected revision. An empty expected revision skips the
// revision check for read-modify-write callers that hold no prior snapshot.
func (f *File[T]) Update(expected Revision, change func(*T) error) (Snapshot[T], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, err := f.readLocked()
	if err != nil {
		return current, err
	}
	if expected != "" && current.Revision != expected {
		return current, ErrConflict
	}
	if !current.Writable {
		return current, ErrReadOnly
	}
	value := current.Value
	if err := change(&value); err != nil {
		return current, err
	}
	if err := f.checkLocked(value); err != nil {
		return current, err
	}
	raw, err := encode(value)
	if err != nil {
		return current, err
	}
	if RevisionOf(raw) != current.Revision {
		if err := WriteFile(f.path, raw); err != nil {
			return current, err
		}
	}
	return f.readLocked()
}

// writable probes whether both the file and its directory accept writes,
// which the atomic replacement requires. Read-only mounts fail either probe.
func writable(path string) bool {
	directory := filepath.Dir(path)
	probe, err := os.CreateTemp(directory, ".config-probe-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	return file.Close() == nil
}

// View is the type-erased JSON form of a Snapshot for generic tools.
type View struct {
	Name     string          `json:"name"`
	Path     string          `json:"path"`
	Value    json.RawMessage `json:"value"`
	Revision Revision        `json:"revision"`
	Loaded   Revision        `json:"loaded"`
	Pending  bool            `json:"pending"`
	Writable bool            `json:"writable"`
	Problems Problems        `json:"problems,omitempty"`
	Advice   Problems        `json:"advice,omitempty"`
	Modified *time.Time      `json:"modified,omitempty"`
	Locks    []Lock          `json:"locks,omitempty"`
}

// Description is the static information a generic editor needs.
type Description struct {
	Name     string          `json:"name"`
	FileName string          `json:"file_name"`
	Schema   Schema          `json:"schema"`
	Defaults json.RawMessage `json:"defaults"`
}

// Entry is a managed document independent of its Go type. Every operation
// takes the caller's section access; AllAccess sees and edits everything.
type Entry interface {
	Name() string
	// Schema is the complete description, from which access is granted.
	Schema() Schema
	Describe(access Access) (Description, error)
	View(access Access) (View, error)
	// CheckJSON returns blocking problems and, for a valid candidate, advice.
	CheckJSON(raw []byte, access Access) (problems, advice Problems)
	ReplaceJSON(raw []byte, expected Revision, access Access) (View, error)
}

func (f *File[T]) Schema() Schema { return f.document.Schema }

func (f *File[T]) Describe(access Access) (Description, error) {
	defaults, err := f.restrictValue(f.document.Defaults(), access)
	return Description{Name: f.document.Name, FileName: f.document.FileName, Schema: f.document.Schema.Restrict(access), Defaults: defaults}, err
}

func (f *File[T]) View(access Access) (View, error) {
	snapshot, err := f.Read()
	if err != nil {
		return View{}, err
	}
	return f.view(snapshot, access)
}

func (f *File[T]) view(snapshot Snapshot[T], access Access) (View, error) {
	value, err := f.restrictValue(snapshot.Value, access)
	if err != nil {
		return View{}, err
	}
	schema := f.document.Schema
	view := View{Name: f.document.Name, Path: f.path, Value: value, Revision: snapshot.Revision, Loaded: snapshot.Loaded,
		Pending: snapshot.Pending(), Writable: snapshot.Writable, Problems: restrictProblems(schema, access, snapshot.Problems)}
	for _, advice := range snapshot.Advice {
		if section, ok := schema.SectionOf(advice.Path); access.All || ok && access.Reads(section) {
			view.Advice = append(view.Advice, advice)
		}
	}
	for _, lock := range f.Locks() {
		if section, ok := schema.SectionOf(lock.Path); access.All || ok && access.Reads(section) {
			view.Locks = append(view.Locks, lock)
		}
	}
	if !access.All {
		writes := false
		for _, section := range schema.Sections {
			writes = writes || access.Writes(section.ID)
		}
		view.Writable = view.Writable && writes
	}
	if !snapshot.Modified.IsZero() {
		modified := snapshot.Modified.UTC()
		view.Modified = &modified
	}
	return view, nil
}

func (f *File[T]) restrictValue(value T, access Access) (json.RawMessage, error) {
	if access.All {
		return json.Marshal(value)
	}
	document, err := normalize(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(project(document, f.document.Schema.paths(access.Reads)))
}

// candidate turns a restricted editor's document into a complete one.
func (f *File[T]) candidate(raw []byte, access Access) ([]byte, Problems) {
	if access.All {
		return raw, nil
	}
	snapshot, err := f.Read()
	if err != nil {
		return nil, Problems{{Rule: RuleCustom, Message: err.Error()}}
	}
	current, err := normalize(snapshot.Value)
	if err != nil {
		return nil, Problems{{Rule: RuleDecode, Message: err.Error()}}
	}
	return merge(f.document.Schema, access, current, raw)
}

func (f *File[T]) CheckJSON(raw []byte, access Access) (Problems, Problems) {
	complete, problems := f.candidate(raw, access)
	if problems != nil {
		return problems, nil
	}
	value, err := f.document.Parse(complete)
	if err == nil {
		err = f.Check(value)
	}
	if err != nil {
		return restrictProblems(f.document.Schema, access, AsProblems(err)), nil
	}
	var advice Problems
	for _, item := range f.document.advise(value, f.path) {
		if section, ok := f.document.Schema.SectionOf(item.Path); access.All || ok && access.Reads(section) {
			advice = append(advice, item)
		}
	}
	return nil, advice
}

func (f *File[T]) ReplaceJSON(raw []byte, expected Revision, access Access) (View, error) {
	complete, problems := f.candidate(raw, access)
	if problems != nil {
		return View{}, problems
	}
	value, err := f.document.Parse(complete)
	if err != nil {
		return View{}, restrictProblems(f.document.Schema, access, AsProblems(err))
	}
	snapshot, err := f.Replace(value, expected)
	var located Problems
	if errors.As(err, &located) {
		return View{}, restrictProblems(f.document.Schema, access, located)
	}
	if err != nil {
		return View{}, err
	}
	return f.view(snapshot, access)
}

// Registry lists managed documents in presentation order.
type Registry struct {
	entries []Entry
}

func NewRegistry(entries ...Entry) *Registry { return &Registry{entries: entries} }

func (r *Registry) Entries() []Entry { return append([]Entry(nil), r.entries...) }

func (r *Registry) Lookup(name string) (Entry, bool) {
	for _, entry := range r.entries {
		if entry.Name() == name {
			return entry, true
		}
	}
	return nil, false
}
