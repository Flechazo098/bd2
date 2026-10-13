package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Document defines one kind of JSON configuration file: where it lives by
// default, what is published when it is missing, how it is decoded and which
// values are valid. Packages declare a Document instead of re-implementing the
// ensure/load/validate sequence.
type Document[T any] struct {
	// Name is the stable identifier used by generic APIs, such as "game".
	Name     string
	FileName string
	// Defaults is written when the file is missing.
	Defaults func() T
	// Base is the value a file overlays while decoding. Nil means Defaults;
	// documents whose omissions must stay empty return the zero value.
	Base   func() T
	Decode DecodeOptions
	Schema Schema
	// Rules adds cross-field validation after the schema's generic checks.
	Rules func(T) Problems
	// Advice reports non-blocking warnings about how a valid value would
	// behave in this process, given the document's absolute path. Startup
	// still decides; advice only lets editors warn before a restart.
	Advice func(value T, path string) Problems
}

func (d Document[T]) advise(value T, path string) Problems {
	if d.Advice == nil {
		return nil
	}
	return d.Advice(value, path)
}

// Validate applies the schema constraints followed by the document rules.
func (d Document[T]) Validate(value T) error {
	problems := d.Schema.Check(value)
	if d.Rules != nil {
		problems = append(problems, d.Rules(value)...)
	}
	return problems.Err()
}

// Parse decodes one complete document using the document's policies and
// validates it. Decoding failures are reported as one document-level problem.
func (d Document[T]) Parse(raw []byte) (T, error) {
	var zero T
	value, err := Decode(raw, d.base(), d.Decode)
	if err != nil {
		return zero, Problems{{Rule: RuleDecode, Message: err.Error()}}
	}
	if problems := d.Schema.checkPresence(raw); len(problems) != 0 {
		return zero, problems
	}
	if err := d.Validate(value); err != nil {
		return zero, err
	}
	return value, nil
}

// Load publishes defaults when path is missing, then strictly parses it.
func (d Document[T]) Load(path string) (T, error) {
	value, _, err := d.load(path)
	return value, err
}

func (d Document[T]) load(path string) (T, []byte, error) {
	var zero T
	if err := Ensure(path, d.Defaults(), WriteOptions{}); err != nil {
		return zero, nil, fmt.Errorf("%s configuration: initialize %s: %w", d.Name, path, err)
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return zero, nil, fmt.Errorf("%s configuration: load %s: %w", d.Name, path, err)
	}
	value, err := d.Parse(raw)
	if err != nil {
		return zero, nil, fmt.Errorf("%s configuration: %s: %w", d.Name, path, err)
	}
	return value, raw, nil
}

// Encode renders value in the canonical on-disk representation.
func (d Document[T]) Encode(value T) ([]byte, error) {
	return encode(value)
}

// Path places the document's file in directory.
func (d Document[T]) Path(directory string) string {
	return filepath.Join(directory, d.FileName)
}

func (d Document[T]) base() T {
	if d.Base != nil {
		return d.Base()
	}
	return d.Defaults()
}

// Open loads path and keeps it under management. The revision that was loaded
// is remembered so callers can tell whether a later edit is already in effect.
func (d Document[T]) Open(path string) (*File[T], T, error) {
	var zero T
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, zero, fmt.Errorf("%s configuration: resolve %s: %w", d.Name, path, err)
	}
	value, raw, err := d.load(absolute)
	if err != nil {
		return nil, zero, err
	}
	return &File[T]{document: d, path: absolute, loaded: RevisionOf(raw)}, value, nil
}

func encode(value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}
