package config

import (
	"encoding/json"
	"strings"
)

// Access limits an editor to some schema sections. Sections are the unit of
// permission: a reader sees only readable sections, and a write may change
// only writable ones. The zero value grants nothing; use AllAccess for owners.
type Access struct {
	All   bool
	Read  map[string]bool
	Write map[string]bool
}

var AllAccess = Access{All: true}

func (a Access) Reads(section string) bool  { return a.All || a.Read[section] || a.Write[section] }
func (a Access) Writes(section string) bool { return a.All || a.Write[section] }

// Any reports whether at least one section of s is readable.
func (a Access) Any(s Schema) bool {
	for _, section := range s.Sections {
		if a.Reads(section.ID) {
			return true
		}
	}
	return false
}

// Restrict returns the readable part of the schema, marking sections that
// may be viewed but not changed.
func (s Schema) Restrict(a Access) Schema {
	if a.All {
		return s
	}
	restricted := s
	restricted.Sections = nil
	for _, section := range s.Sections {
		if a.Reads(section.ID) {
			section.ReadOnly = !a.Writes(section.ID)
			restricted.Sections = append(restricted.Sections, section)
		}
	}
	return restricted
}

// SectionOf returns the section whose field owns a problem or lock path.
func (s Schema) SectionOf(path string) (string, bool) {
	bare := stripIndexes(path)
	parts := strings.Split(bare, ".")
	best, bestLength := "", -1
	for _, section := range s.Sections {
		for _, field := range section.Fields {
			pattern := strings.Split(field.Path, ".")
			if len(pattern) > len(parts) || len(pattern) <= bestLength {
				continue
			}
			matched := true
			for i, segment := range pattern {
				if segment != "*" && segment != parts[i] {
					matched = false
					break
				}
			}
			if matched {
				best, bestLength = section.ID, len(pattern)
			}
		}
	}
	return best, bestLength >= 0
}

func stripIndexes(path string) string {
	var out strings.Builder
	depth := 0
	for _, r := range path {
		switch {
		case r == '[':
			depth++
		case r == ']':
			depth--
		case depth == 0:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// paths lists the concrete field paths of the selected sections. Wildcard
// fields belong to the map field that contains them.
func (s Schema) paths(selected func(string) bool) []string {
	var out []string
	for _, section := range s.Sections {
		if !selected(section.ID) {
			continue
		}
		for _, field := range section.Fields {
			if !strings.Contains(field.Path, "*") {
				out = append(out, field.Path)
			}
		}
	}
	return out
}

// project copies only the given paths of a normalized document.
func project(document any, paths []string) map[string]any {
	out := map[string]any{}
	for _, path := range paths {
		if value, ok := getPath(document, path); ok {
			setPath(out, path, value)
		}
	}
	return out
}

func getPath(document any, path string) (any, bool) {
	current := document
	for segment := range strings.SplitSeq(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		if current, ok = object[segment]; !ok {
			return nil, false
		}
	}
	return current, true
}

func setPath(document map[string]any, path string, value any) {
	keys := strings.Split(path, ".")
	node := document
	for _, key := range keys[:len(keys)-1] {
		child, ok := node[key].(map[string]any)
		if !ok {
			child = map[string]any{}
			node[key] = child
		}
		node = child
	}
	node[keys[len(keys)-1]] = value
}

func deletePath(document map[string]any, path string) {
	keys := strings.Split(path, ".")
	node := document
	for _, key := range keys[:len(keys)-1] {
		child, ok := node[key].(map[string]any)
		if !ok {
			return
		}
		node = child
	}
	delete(node, keys[len(keys)-1])
}

// merge applies a restricted editor's candidate to the current document.
// Writable fields are taken from the candidate; a readable field that the
// candidate changes without write access is a problem; fields the editor
// cannot see are kept from the current document.
func merge(schema Schema, a Access, current any, raw []byte) ([]byte, Problems) {
	var candidate any
	if err := json.Unmarshal(raw, &candidate); err != nil {
		return nil, Problems{{Rule: RuleDecode, Message: err.Error()}}
	}
	result, ok := current.(map[string]any)
	if !ok {
		result = map[string]any{}
	}
	var problems Problems
	for _, section := range schema.Sections {
		if !a.Reads(section.ID) {
			continue
		}
		for _, path := range schema.paths(func(id string) bool { return id == section.ID }) {
			next, present := getPath(candidate, path)
			previous, existed := getPath(current, path)
			if !a.Writes(section.ID) {
				if present != existed || present && !sameJSON(next, previous) {
					problems.Add(path, RuleForbidden, "no permission to change this section")
				}
				continue
			}
			if present {
				setPath(result, path, next)
			} else {
				deletePath(result, path)
			}
		}
	}
	if len(problems) != 0 {
		return nil, problems
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, Problems{{Rule: RuleDecode, Message: err.Error()}}
	}
	return encoded, nil
}

// restrictProblems keeps problems located in readable sections. Problems the
// editor cannot see are summarized so a blocked save is still explained.
func restrictProblems(schema Schema, a Access, problems Problems) Problems {
	if a.All || len(problems) == 0 {
		return problems
	}
	var out Problems
	hidden := false
	for _, problem := range problems {
		section, ok := schema.SectionOf(problem.Path)
		if problem.Path == "" || ok && a.Reads(section) {
			out = append(out, problem)
		} else {
			hidden = true
		}
	}
	if hidden {
		out.Add("", RuleForbidden, "a section outside your access is invalid")
	}
	return out
}
