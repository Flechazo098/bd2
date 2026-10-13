package config

import (
	"errors"
	"fmt"
	"strings"
)

// Problem locates one rejected value inside a configuration document. Path
// uses the schema path syntax: dot-separated JSON names, "*" for map keys and
// "[]" for list items, with concrete keys and indexes filled in.
type Problem struct {
	Path    string `json:"path"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

const (
	RuleEnum     = "enum"
	RuleRange    = "range"
	RuleRequired = "required"
	RuleUnique   = "unique"
	RuleFormat   = "format"
	RuleConflict = "conflict"
	RuleDecode   = "decode"
	RuleCustom   = "custom"
	// RuleForbidden marks a change outside the editor's granted sections.
	RuleForbidden = "forbidden"
	// RuleEnvironment marks advice about the running process rather than the
	// document, such as a referenced environment variable that is unset.
	RuleEnvironment = "environment"
	// RuleMissingFile marks advice about a referenced file that does not exist.
	RuleMissingFile = "missing_file"
)

// Problems is a validation result. A non-empty value is an error so existing
// Validate() error contracts keep working while callers that render forms can
// recover every located problem with AsProblems.
type Problems []Problem

func (p Problems) Error() string {
	parts := make([]string, len(p))
	for i, problem := range p {
		if problem.Path == "" {
			parts[i] = problem.Message
		} else {
			parts[i] = problem.Path + ": " + problem.Message
		}
	}
	return strings.Join(parts, "; ")
}

// Add records a problem using fmt formatting for the message.
func (p *Problems) Add(path, rule, format string, args ...any) {
	*p = append(*p, Problem{Path: path, Rule: rule, Message: fmt.Sprintf(format, args...)})
}

// Err returns nil for an empty result so functions can end with problems.Err().
func (p Problems) Err() error {
	if len(p) == 0 {
		return nil
	}
	return p
}

// Invalid reports one custom problem at path.
func Invalid(path, format string, args ...any) error {
	var problems Problems
	problems.Add(path, RuleCustom, format, args...)
	return problems
}

// AsProblems recovers located problems from err. Errors that carry no
// location become one document-level problem.
func AsProblems(err error) Problems {
	if err == nil {
		return nil
	}
	var problems Problems
	if errors.As(err, &problems) {
		return problems
	}
	return Problems{{Rule: RuleCustom, Message: err.Error()}}
}
