package config

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
)

// Kind is the JSON shape of a described field.
type Kind string

const (
	KindBool    Kind = "bool"
	KindInteger Kind = "integer"
	KindString  Kind = "string"
	KindList    Kind = "list"
	KindMap     Kind = "map"
)

// Schema describes a document for generic editors. Generic constraints
// (options, ranges, uniqueness and map keys) are enforced by Check, so a
// document states each such rule once; cross-field rules remain in the
// document's own validation function.
type Schema struct {
	Title    string    `json:"title"`
	Summary  string    `json:"summary,omitempty"`
	Sections []Section `json:"sections"`
}

type Section struct {
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	Help   string  `json:"help,omitempty"`
	Fields []Field `json:"fields"`
}

// Field describes one value. Path uses dot-separated JSON names; "*" selects
// every map key and "[]" every list item.
type Field struct {
	Path        string `json:"path"`
	Kind        Kind   `json:"kind"`
	Label       string `json:"label"`
	Help        string `json:"help,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	// Format is an editor hint such as "duration", "origin", "url", "host",
	// "listen", "env", "path" or "identifier". It is not enforced generically.
	Format string `json:"format,omitempty"`
	// Options lists allowed values. For lists they constrain each item.
	Options []Option `json:"options,omitempty"`
	// OpenOptions turns Options into suggestions that do not restrict input.
	OpenOptions bool   `json:"open_options,omitempty"`
	Min         *int64 `json:"min,omitempty"`
	Max         *int64 `json:"max,omitempty"`
	Unique      bool   `json:"unique,omitempty"`
	// Required means the value must be written explicitly in the file; a
	// default supplied by decoding does not satisfy it.
	Required bool       `json:"required,omitempty"`
	Keys     []Option   `json:"keys,omitempty"`
	Advanced bool       `json:"advanced,omitempty"`
	When     *Condition `json:"when,omitempty"`
}

type Option struct {
	Value any    `json:"value"`
	Label string `json:"label"`
	Help  string `json:"help,omitempty"`
}

// Condition limits a field to documents whose value at Path equals one of In.
// Paths are absolute and must not contain wildcards.
type Condition struct {
	Path string `json:"path"`
	In   []any  `json:"in"`
	Not  bool   `json:"not,omitempty"`
}

// Range returns pointers for Field.Min and Field.Max.
func Range(minimum, maximum int64) (*int64, *int64) { return &minimum, &maximum }

// Fields returns every described field in declaration order.
func (s Schema) Fields() []Field {
	var fields []Field
	for _, section := range s.Sections {
		fields = append(fields, section.Fields...)
	}
	return fields
}

// Check enforces the generic constraints of every applicable field.
func (s Schema) Check(value any) Problems {
	document, err := normalize(value)
	if err != nil {
		return Problems{{Rule: RuleDecode, Message: err.Error()}}
	}
	var problems Problems
	for _, field := range s.Fields() {
		if field.When != nil && !field.When.holds(document) {
			continue
		}
		for _, found := range lookup(document, field.Path) {
			field.check(found.path, found.value, &problems)
		}
	}
	return problems
}

// checkPresence reports Required fields absent from a raw document. For
// wildcard paths every existing parent must contain the field.
func (s Schema) checkPresence(raw []byte) Problems {
	var document any
	var problems Problems
	for _, field := range s.Fields() {
		if !field.Required {
			continue
		}
		if document == nil {
			if err := json.Unmarshal(raw, &document); err != nil {
				return Problems{{Rule: RuleDecode, Message: err.Error()}}
			}
		}
		parentPath, name := "", field.Path
		if i := strings.LastIndex(field.Path, "."); i >= 0 {
			parentPath, name = field.Path[:i], field.Path[i+1:]
		}
		parents := []located{{value: document}}
		if parentPath != "" {
			parents = lookup(document, parentPath)
		}
		for _, parent := range parents {
			if object, ok := parent.value.(map[string]any); ok {
				if _, exists := object[name]; !exists {
					problems.Add(join(parent.path, name), RuleRequired, "must be present")
				}
			}
		}
	}
	return problems
}

func (c Condition) holds(document any) bool {
	found := lookup(document, c.Path)
	matched := len(found) == 1 && slices.ContainsFunc(c.In, func(candidate any) bool { return sameJSON(found[0].value, candidate) })
	return matched != c.Not
}

func (f Field) check(path string, value any, problems *Problems) {
	switch f.Kind {
	case KindList:
		items, ok := value.([]any)
		if !ok {
			problems.Add(path, RuleDecode, "must be a list")
			return
		}
		seen := map[string]bool{}
		for i, item := range items {
			itemPath := fmt.Sprintf("%s[%d]", path, i)
			f.checkOption(itemPath, item, problems)
			if f.Unique {
				key := canonicalJSON(item)
				if seen[key] {
					problems.Add(itemPath, RuleUnique, "duplicate value %s", key)
				}
				seen[key] = true
			}
		}
	case KindMap:
		entries, ok := value.(map[string]any)
		if !ok {
			problems.Add(path, RuleDecode, "must be an object")
			return
		}
		if len(f.Keys) == 0 {
			return
		}
		names := make([]string, 0, len(entries))
		for name := range entries {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if !slices.ContainsFunc(f.Keys, func(option Option) bool { return sameJSON(name, option.Value) }) {
				problems.Add(path+"."+name, RuleEnum, "unsupported key %q", name)
			}
		}
	default:
		f.checkOption(path, value, problems)
		if f.Min != nil || f.Max != nil {
			number, ok := value.(float64)
			if !ok || number != math.Trunc(number) || f.Min != nil && number < float64(*f.Min) || f.Max != nil && number > float64(*f.Max) {
				problems.Add(path, RuleRange, "must be an integer between %s and %s", bound(f.Min, "-inf"), bound(f.Max, "+inf"))
			}
		}
	}
}

func (f Field) checkOption(path string, value any, problems *Problems) {
	if len(f.Options) == 0 || f.OpenOptions {
		return
	}
	if slices.ContainsFunc(f.Options, func(option Option) bool { return sameJSON(value, option.Value) }) {
		return
	}
	allowed := make([]string, len(f.Options))
	for i, option := range f.Options {
		allowed[i] = canonicalJSON(option.Value)
	}
	problems.Add(path, RuleEnum, "must be one of %s", strings.Join(allowed, ", "))
}

func bound(value *int64, missing string) string {
	if value == nil {
		return missing
	}
	return fmt.Sprint(*value)
}

type located struct {
	path  string
	value any
}

// lookup expands a schema path against a normalized JSON document. Absent
// values are omitted: optional fields are checked only when present.
func lookup(document any, path string) []located {
	current := []located{{value: document}}
	for segment := range strings.SplitSeq(path, ".") {
		list := strings.HasSuffix(segment, "[]")
		name := strings.TrimSuffix(segment, "[]")
		var next []located
		for _, item := range current {
			object, ok := item.value.(map[string]any)
			if !ok {
				continue
			}
			var children []located
			if name == "*" {
				keys := make([]string, 0, len(object))
				for key := range object {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					children = append(children, located{join(item.path, key), object[key]})
				}
			} else if child, exists := object[name]; exists {
				children = append(children, located{join(item.path, name), child})
			}
			for _, child := range children {
				if !list {
					next = append(next, child)
					continue
				}
				values, ok := child.value.([]any)
				if !ok {
					continue
				}
				for i, value := range values {
					next = append(next, located{fmt.Sprintf("%s[%d]", child.path, i), value})
				}
			}
		}
		current = next
	}
	return current
}

func join(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

func normalize(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var document any
	return document, json.Unmarshal(raw, &document)
}

func canonicalJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(raw)
}

func sameJSON(a, b any) bool { return canonicalJSON(a) == canonicalJSON(b) }
