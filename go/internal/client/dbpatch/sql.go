package dbpatch

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type token struct {
	text string
	kind byte
}

// SQLite permits quoted identifiers and semicolons inside literals. Tokenize
// those forms before enforcing a single data statement, rather than splitting SQL.
func sqlTokens(source string) ([]token, error) {
	var out []token
	for i := 0; i < len(source); {
		c := source[i]
		switch {
		case c <= ' ':
			i++
		case c == '-' && i+1 < len(source) && source[i+1] == '-':
			for i < len(source) && source[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(source) && source[i+1] == '*':
			end := strings.Index(source[i+2:], "*/")
			if end < 0 {
				return nil, errors.New("unterminated SQL comment")
			}
			i += end + 4
		case c == '\'' || c == '"' || c == '`' || c == '[':
			endQuote := c
			if c == '[' {
				endQuote = ']'
			}
			kind := byte('i')
			if c == '\'' {
				kind = 's'
			}
			i++
			var value strings.Builder
			closed := false
			for i < len(source) {
				if source[i] == endQuote {
					if endQuote != ']' && i+1 < len(source) && source[i+1] == endQuote {
						value.WriteByte(endQuote)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				value.WriteByte(source[i])
				i++
			}
			if !closed {
				return nil, errors.New("unterminated SQL quote")
			}
			out = append(out, token{strings.ToLower(value.String()), kind})
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_':
			start := i
			for i < len(source) && (source[i] >= 'a' && source[i] <= 'z' || source[i] >= 'A' && source[i] <= 'Z' || source[i] >= '0' && source[i] <= '9' || source[i] == '_') {
				i++
			}
			out = append(out, token{strings.ToLower(source[start:i]), 'i'})
		default:
			if c == 0 || c >= 128 {
				return nil, errors.New("SQL uses an unsupported unquoted character")
			}
			out = append(out, token{string(c), 'p'})
			i++
		}
	}
	return out, nil
}

func validateSQL(source string, assertion bool) error {
	tokens, err := sqlTokens(source)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		return errors.New("empty SQL statement")
	}
	if tokens[len(tokens)-1].text == ";" {
		tokens = tokens[:len(tokens)-1]
	}
	if len(tokens) == 0 {
		return errors.New("empty SQL statement")
	}
	verb := tokens[0].text
	if tokens[0].kind != 'i' || assertion && verb != "select" || !assertion && verb != "update" && verb != "insert" && verb != "delete" {
		return errors.New("patch SQL must be UPDATE, INSERT or DELETE; assertions must be SELECT")
	}
	for _, t := range tokens {
		if t.kind == 'p' && (t.text == ";" || t.text == "?" || t.text == "@" || t.text == "$") {
			return errors.New("multiple SQL statements and positional parameters are forbidden")
		}
		if t.kind != 'i' {
			continue
		}
		if strings.HasPrefix(t.text, "_bd2_") || strings.HasPrefix(t.text, "pragma_") || !assertion && strings.HasPrefix(t.text, "sqlite_") {
			return errors.New("patch SQL cannot access migration metadata, PRAGMA or SQLite internals")
		}
		switch t.text {
		case "attach", "detach", "pragma", "vacuum", "create", "alter", "drop", "reindex", "analyze", "begin", "commit", "rollback", "savepoint", "release", "with", "load_extension", "readfile", "writefile", "fts3_tokenizer", "eval":
			return fmt.Errorf("forbidden SQL operation %q", t.text)
		}
	}
	targetIndex := -1
	if !assertion {
		at := 1
		if verb == "insert" || verb == "update" {
			if at < len(tokens) && tokens[at].text == "or" {
				at += 2
			}
		}
		if verb == "insert" || verb == "delete" {
			keyword := "into"
			if verb == "delete" {
				keyword = "from"
			}
			if at >= len(tokens) || tokens[at].text != keyword {
				return errors.New("unsupported SQL target")
			}
			at++
		}
		if at >= len(tokens) || tokens[at].kind != 'i' {
			return errors.New("SQL table must be an identifier")
		}
		if at+1 < len(tokens) && tokens[at+1].text == "." && (at+2 >= len(tokens) || tokens[at+2].kind != 'i') {
			return errors.New("qualified SQL table must be an identifier")
		}
		targetIndex = at
		if at+1 < len(tokens) && tokens[at+1].text == "." {
			targetIndex = at + 2
		}
	}
	for i, t := range tokens {
		if t.kind != 'i' || i+1 >= len(tokens) || tokens[i+1].text != "(" {
			continue
		}
		// An INSERT table name is followed by its column list, not a function.
		if verb == "insert" && i == targetIndex {
			continue
		}
		switch t.text {
		// Keywords that open a subquery, row value or expression group.
		case "select", "from", "join", "using", "where", "and", "or", "not", "is", "like", "glob", "between", "case", "when", "then", "else", "on", "set", "by", "distinct", "all", "having", "limit", "offset", "filter", "over", "partition", "as", "values", "in", "exists":
		// Functions.
		case "cast", "count", "sum", "total", "avg", "min", "max", "abs", "coalesce", "ifnull", "nullif", "iif", "typeof", "length", "hex", "unhex", "lower", "upper", "substr", "substring", "trim", "ltrim", "rtrim", "replace", "round", "instr", "printf", "format", "quote", "unicode", "char", "changes", "json", "json_valid", "json_extract", "json_type", "json_array_length", "json_array", "json_object", "json_set", "json_insert", "json_replace", "json_remove", "json_patch", "json_each", "json_tree", "json_group_array", "json_group_object":
		default:
			return fmt.Errorf("SQL function %q is not allowed in data migrations", t.text)
		}
	}
	return nil
}

func parameters(values map[string]any, owner int64) ([]any, error) {
	args := []any{sql.Named("owner", owner)}
	for name, value := range values {
		if name == "owner" || name == "" {
			return nil, errors.New("owner is a reserved SQL parameter")
		}
		for _, c := range name {
			valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
			if !valid {
				return nil, errors.New("invalid SQL parameter name")
			}
		}
		switch v := value.(type) {
		case json.Number:
			integer, err := v.Int64()
			if err == nil {
				value = integer
			} else {
				if !strings.ContainsAny(v.String(), ".eE") {
					return nil, errors.New("integer parameter exceeds SQLite signed 64-bit range")
				}
				decimal, err := v.Float64()
				if err != nil {
					return nil, err
				}
				value = decimal
			}
		case nil, string, bool, int, int64, float64:
		default:
			return nil, fmt.Errorf("SQL parameter %s is not a scalar", name)
		}
		args = append(args, sql.Named(name, value))
	}
	return args, nil
}
