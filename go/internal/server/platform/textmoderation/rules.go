package textmoderation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"bd2server/internal/server/design/gameconfig"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const maxRuleFileBytes = 4 << 20

type ruleDefinition struct {
	Text       string   `json:"text"`
	Match      string   `json:"match"`
	Scopes     []string `json:"scopes"`
	Exceptions []string `json:"exceptions"`
}

type rule struct {
	text, match        string
	scopes, exceptions map[string]bool
}

func matchingKey(text string) string {
	return norm.NFKC.String(cases.Fold().String(norm.NFKC.String(text)))
}

func loadRules(paths []string, baseDir string) ([]rule, error) {
	var out []rule
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		path = filepath.Clean(path)
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("text moderation: open rule file %q: %w", path, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxRuleFileBytes+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return nil, fmt.Errorf("text moderation: read rule file %q: %w", path, errors.Join(readErr, closeErr))
		}
		if len(data) > maxRuleFileBytes {
			return nil, fmt.Errorf("text moderation: rule file %q exceeds %d bytes", path, maxRuleFileBytes)
		}
		data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
		if !utf8.Valid(data) {
			return nil, fmt.Errorf("text moderation: rule file %q must be UTF-8", path)
		}
		var definitions []ruleDefinition
		switch strings.ToLower(filepath.Ext(path)) {
		case ".json":
			var shape any
			if json.Unmarshal(data, &shape) != nil || containsNull(shape) {
				return nil, fmt.Errorf("text moderation: invalid or null JSON rule data in %q", path)
			}
			var document struct {
				Rules *[]ruleDefinition `json:"rules"`
			}
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&document); err != nil {
				return nil, fmt.Errorf("text moderation: invalid JSON rule file %q", path)
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				return nil, fmt.Errorf("text moderation: trailing JSON rule data in %q", path)
			}
			if document.Rules == nil {
				return nil, fmt.Errorf("text moderation: JSON rules array is required in %q", path)
			}
			definitions = *document.Rules
		case ".txt":
			for line := range strings.SplitSeq(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					definitions = append(definitions, ruleDefinition{Text: line, Match: "substring"})
				}
			}
		default:
			return nil, fmt.Errorf("text moderation: rule file %q must use .txt or .json", path)
		}
		for index, definition := range definitions {
			compiled, err := compileRule(definition)
			if err != nil {
				return nil, fmt.Errorf("text moderation: rule file %q rule %d: %w", path, index+1, err)
			}
			out = append(out, compiled)
		}
	}
	return out, nil
}

func containsNull(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case map[string]any:
		for _, child := range value {
			if containsNull(child) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(value, containsNull)
	}
	return false
}

func compileRule(d ruleDefinition) (rule, error) {
	r := rule{text: matchingKey(d.Text), match: d.Match, scopes: map[string]bool{}, exceptions: map[string]bool{}}
	if strings.TrimSpace(r.text) == "" || utf8.RuneCountInString(r.text) > 4096 {
		return rule{}, fmt.Errorf("text moderation: empty or excessive rule text")
	}
	if r.match != "exact" && r.match != "word" && r.match != "substring" {
		return rule{}, fmt.Errorf("text moderation: rule match must be exact, word, or substring")
	}
	for _, scope := range d.Scopes {
		if !gameconfig.ValidTextScope(scope) || r.scopes[scope] {
			return rule{}, fmt.Errorf("text moderation: invalid or duplicate rule scope")
		}
		r.scopes[scope] = true
	}
	for _, exception := range d.Exceptions {
		key := matchingKey(exception)
		if key == "" {
			return rule{}, fmt.Errorf("text moderation: empty rule exception")
		}
		r.exceptions[key] = true
	}
	return r, nil
}

func (r rule) matchesAt(scope, key string, start, end int) bool {
	if len(r.scopes) > 0 && !r.scopes[scope] || r.exceptions[key] {
		return false
	}
	switch r.match {
	case "exact":
		return start == 0 && end == len(key)
	case "substring":
		return true
	case "word":
		before, _ := utf8.DecodeLastRuneInString(key[:start])
		after, _ := utf8.DecodeRuneInString(key[end:])
		return (start == 0 || !wordRune(before)) && (end == len(key) || !wordRune(after))
	}
	return false
}

func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || r == '_'
}
