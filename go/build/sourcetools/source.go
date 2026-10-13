// Package sourcetools creates readable mirrors and member-level reports for
// decompiled C# source. It never writes into an input source tree.
package sourcetools

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

func identifierStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.In(r, unicode.Nl)
}
func identifierContinue(r rune) bool {
	return identifierStart(r) || unicode.In(r, unicode.Mn, unicode.Mc, unicode.Nd, unicode.Pc, unicode.Cf)
}
func identifier(s string) bool {
	s = strings.TrimPrefix(s, "@")
	r := []rune(s)
	if len(r) == 0 || !identifierStart(r[0]) {
		return false
	}
	for _, c := range r[1:] {
		if !identifierContinue(c) {
			return false
		}
	}
	return true
}

// Lexing keeps comments and all literal payloads opaque, including raw strings.
type token struct{ text, kind string }

func lex(s string) []token {
	s = strings.TrimPrefix(s, "\ufeff")
	var result []token
	for i := 0; i < len(s); {
		start := i
		if strings.HasPrefix(s[i:], "//") {
			if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
				i += j
			} else {
				i = len(s)
			}
			result = append(result, token{s[start:i], "comment"})
			continue
		}
		if strings.HasPrefix(s[i:], "/*") {
			if j := strings.Index(s[i+2:], "*/"); j >= 0 {
				i += j + 4
			} else {
				i = len(s)
			}
			result = append(result, token{s[start:i], "comment"})
			continue
		}
		q := i
		for q < len(s) && (s[q] == '$' || s[q] == '@') {
			q++
		}
		if q < len(s) && (s[q] == '"' || s[q] == '\'') && (q == i || s[q] == '"') {
			quote := s[q]
			count := 1
			for q+count < len(s) && s[q+count] == quote {
				count++
			}
			if quote == '"' && count >= 3 {
				marker := strings.Repeat("\"", count)
				if j := strings.Index(s[q+count:], marker); j >= 0 {
					i = q + count + j + count
				} else {
					i = len(s)
				}
			} else {
				verbatim := strings.Contains(s[start:q], "@")
				i = q + 1
				for i < len(s) {
					if verbatim && quote == '"' && strings.HasPrefix(s[i:], "\"\"") {
						i += 2
						continue
					}
					if s[i] == quote {
						i++
						break
					}
					if !verbatim && s[i] == '\\' {
						i += 2
					} else {
						i++
					}
				}
				if i > len(s) {
					i = len(s)
				}
			}
			result = append(result, token{s[start:i], "literal"})
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if unicode.IsSpace(r) {
			i += size
			result = append(result, token{s[start:i], "space"})
			continue
		}
		at := r == '@'
		if at && i+size < len(s) {
			r, _ = utf8.DecodeRuneInString(s[i+size:])
		}
		if identifierStart(r) {
			i += size
			if at {
				_, n := utf8.DecodeRuneInString(s[i:])
				i += n
			}
			for i < len(s) {
				c, n := utf8.DecodeRuneInString(s[i:])
				if !identifierContinue(c) {
					break
				}
				i += n
			}
			result = append(result, token{s[start:i], "identifier"})
			continue
		}
		if s[i] >= '0' && s[i] <= '9' || s[i] == '.' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
			i++
			for i < len(s) {
				c := s[i]
				if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '.' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
					i++
					continue
				}
				if (c == '+' || c == '-') && (s[i-1] == 'e' || s[i-1] == 'E') {
					i++
					continue
				}
				break
			}
			result = append(result, token{s[start:i], "number"})
			continue
		}
		op := ""
		for _, v := range []string{"??=", "<<=", ">>=", "=>", "==", "!=", "<=", ">=", "&&", "||", "++", "--", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "??", "?.", "::", "->"} {
			if strings.HasPrefix(s[i:], v) {
				op = v
				break
			}
		}
		if op != "" {
			i += len(op)
		} else {
			i += size
		}
		result = append(result, token{s[start:i], "operator"})
	}
	return result
}

func readSource(path string) (string, string, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return "", "", e
	}
	if len(b) >= 2 && (b[0] == 0xff && b[1] == 0xfe || b[0] == 0xfe && b[1] == 0xff) {
		if len(b)%2 != 0 {
			return "", "", errors.New("invalid UTF-16 length")
		}
		enc := "utf-16-le"
		var order binary.ByteOrder = binary.LittleEndian
		if b[0] == 0xfe {
			enc = "utf-16-be"
			order = binary.BigEndian
		}
		words := make([]uint16, (len(b)-2)/2)
		for i := range words {
			words[i] = order.Uint16(b[2+i*2:])
		}
		for i := 0; i < len(words); i++ {
			if words[i] >= 0xd800 && words[i] <= 0xdbff {
				if i+1 == len(words) || words[i+1] < 0xdc00 || words[i+1] > 0xdfff {
					return "", "", errors.New("invalid UTF-16 surrogate")
				}
				i++
			} else if words[i] >= 0xdc00 && words[i] <= 0xdfff {
				return "", "", errors.New("invalid UTF-16 surrogate")
			}
		}
		return string(utf16.Decode(words)), enc, nil
	}
	enc := "utf-8"
	if len(b) >= 3 && string(b[:3]) == "\xef\xbb\xbf" {
		enc = "utf-8-sig"
		b = b[3:]
	}
	if !utf8.Valid(b) {
		return "", "", errors.New("unsupported text encoding")
	}
	return string(b), enc, nil
}
func writeSource(path, text, enc string) error {
	var b []byte
	switch enc {
	case "utf-16-le", "utf-16-be":
		words := utf16.Encode([]rune(text))
		b = make([]byte, 2+len(words)*2)
		var order binary.ByteOrder = binary.LittleEndian
		b[0], b[1] = 0xff, 0xfe
		if enc == "utf-16-be" {
			order = binary.BigEndian
			b[0], b[1] = 0xfe, 0xff
		}
		for i, w := range words {
			order.PutUint16(b[2+i*2:], w)
		}
	case "utf-8-sig":
		b = append([]byte{0xef, 0xbb, 0xbf}, []byte(text)...)
	default:
		b = []byte(text)
	}
	return os.WriteFile(path, b, 0644)
}

func canonicalPath(path string) (string, error) {
	absolute, e := filepath.Abs(path)
	if e != nil {
		return "", e
	}
	p := absolute
	var suffix []string
	for {
		resolved, e := filepath.EvalSymlinks(p)
		if e == nil {
			for _, name := range slices.Backward(suffix) {
				resolved = filepath.Join(resolved, name)
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(e) {
			return "", e
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", e
		}
		suffix = append(suffix, filepath.Base(p))
		p = parent
	}
}
func within(child, parent string) bool {
	r, e := filepath.Rel(parent, child)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}
func sourceRoot(path string) (string, error) {
	p, e := canonicalPath(path)
	if e != nil {
		return "", e
	}
	s, e := os.Stat(p)
	if e != nil {
		return "", e
	}
	if !s.IsDir() {
		return "", fmt.Errorf("source is not a directory: %s", p)
	}
	return p, nil
}
func safeTrees(source, output string) (string, string, error) {
	s, e := sourceRoot(source)
	if e != nil {
		return "", "", e
	}
	o, e := canonicalPath(output)
	if e != nil {
		return "", "", e
	}
	if within(s, o) || within(o, s) {
		return "", "", errors.New("output must be outside, and not contain, source")
	}
	return s, o, nil
}
func copyFile(source, target string) error {
	b, e := os.ReadFile(source)
	if e != nil {
		return e
	}
	return os.WriteFile(target, b, 0644)
}
