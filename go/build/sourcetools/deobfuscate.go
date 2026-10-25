package sourcetools

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const mirrorTool = "bd2.deobfuscate_client_source"
const mirrorManifest = ".bd2-deobfuscate-manifest.json"

var keywords = wordSet("abstract as base bool break byte case catch char checked class const continue decimal default delegate do double else enum event explicit extern false finally fixed float for foreach goto if implicit in int interface internal is lock long namespace new null object operator out override params private protected public readonly ref return sbyte sealed short sizeof stackalloc static string struct switch this throw true try typeof uint ulong unchecked unsafe ushort using virtual void volatile while")

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for w := range strings.FieldsSeq(s) {
		m[w] = true
	}
	return m
}

type mapEntry struct {
	Line        int    `json:"line"`
	Source      string `json:"source"`
	Meaning     string `json:"meaning"`
	Replacement string `json:"replacement,omitempty"`
	Sanitized   bool   `json:"sanitized"`
	Collision   bool   `json:"name_collision"`
}

func parseMapping(path string) ([]mapEntry, []string, error) {
	text, _, e := readSource(path)
	if e != nil {
		return nil, nil, e
	}
	entries := []mapEntry{}
	warnings := []string{}
	for i, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") || strings.EqualFold(line, "#ReverseOrder") {
			continue
		}
		a, b, ok := strings.Cut(line, "⇨")
		if !ok {
			if !strings.HasPrefix(line, "#") {
				warnings = append(warnings, fmt.Sprintf("line %d: ignored (no '⇨')", i+1))
			}
			continue
		}
		a, b = strings.TrimSpace(a), strings.TrimSpace(b)
		if a == "" || b == "" {
			warnings = append(warnings, fmt.Sprintf("line %d: ignored (empty mapping side)", i+1))
			continue
		}
		if !identifier(a) || strings.HasPrefix(a, "@") {
			warnings = append(warnings, fmt.Sprintf("line %d: ignored (non-identifier source %q)", i+1, a))
			continue
		}
		entries = append(entries, mapEntry{Line: i + 1, Source: a, Meaning: b})
	}
	return entries, warnings, nil
}

var meaningParts = regexp.MustCompile(`(?:\.|::|/|\\)+`)
var asciiSuffix = regexp.MustCompile(`[^A-Za-z0-9_]`)

func readableIdentifier(meaning string) string {
	parts := meaningParts.Split(strings.TrimSpace(meaning), -1)
	raw := ""
	for _, p := range parts {
		if p != "" {
			raw = p
		}
	}
	var b strings.Builder
	for i, c := range raw {
		if c == '_' || c < 128 && unicode.IsLetter(c) || i > 0 && c >= '0' && c <= '9' {
			b.WriteRune(c)
		} else if i == 0 && c >= '0' && c <= '9' {
			b.WriteByte('_')
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	// Leading and trailing underscores are part of the name: the game SDK and
	// BD2.GameNames resolve members by the mapped name, so CommonPacket's
	// _itemKeepInfoDic must read as _itemKeepInfoDic here too.
	name := b.String()
	if strings.Trim(name, "_") == "" {
		name = "unnamed"
	}
	if name[0] >= '0' && name[0] <= '9' || keywords[name] {
		name = "_" + name
	}
	return name
}
func buildReplacements(entries []mapEntry) (map[string]string, []mapEntry, []string) {
	groups := map[string][]mapEntry{}
	order := []string{}
	for _, e := range entries {
		if _, ok := groups[e.Source]; !ok {
			order = append(order, e.Source)
		}
		groups[e.Source] = append(groups[e.Source], e)
	}
	replacements := map[string]string{}
	report := []mapEntry{}
	warnings := []string{}
	used := map[string]bool{}
	for _, source := range order {
		group := groups[source]
		meanings := map[string]bool{}
		for _, e := range group {
			meanings[e.Meaning] = true
		}
		if len(meanings) != 1 {
			warnings = append(warnings, fmt.Sprintf("source %q: ignored ambiguous scoped mappings (%d meanings)", source, len(meanings)))
			continue
		}
		e := group[0]
		base := readableIdentifier(e.Meaning)
		replacement := base
		e.Collision = used[replacement]
		if e.Collision {
			suffix := asciiSuffix.ReplaceAllString(source, "_")
			if strings.Trim(suffix, "_") == "" {
				parts := []string{}
				for _, c := range source {
					parts = append(parts, fmt.Sprintf("u%04X", c))
				}
				suffix = strings.Join(parts, "_")
			}
			replacement = base + "__from_" + suffix
			for n := 2; used[replacement]; n++ {
				replacement = fmt.Sprintf("%s__from_%s_%d", base, suffix, n)
			}
		}
		used[replacement] = true
		replacements[source] = replacement
		e.Replacement = replacement
		e.Sanitized = replacement != e.Meaning
		report = append(report, e)
	}
	return replacements, report, warnings
}
func camel(s string) string {
	r := []rune(s)
	i := 0
	for i < len(r) && unicode.IsUpper(r[i]) {
		r[i] = unicode.ToLower(r[i])
		i++
	}
	return string(r)
}

type replacementIndex struct {
	exact   map[string]string
	derived map[string][]string
}

func newReplacementIndex(m map[string]string) *replacementIndex {
	idx := &replacementIndex{m, map[string][]string{}}
	for s := range m {
		if c := camel(s); c != s {
			idx.derived[c] = append(idx.derived[c], s)
		}
	}
	return idx
}
func (idx *replacementIndex) base(t string) (string, bool) {
	if r, ok := idx.exact[t]; ok {
		return r, false
	}
	if s := idx.derived[t]; len(s) == 1 {
		return camel(idx.exact[s[0]]), true
	}
	return "", false
}
func (idx *replacementIndex) resolve(t string) (string, bool) {
	if r, d := idx.base(t); r != "" {
		return r, d
	}
	if len(t) == utf8.RuneCountInString(t) {
		return t, false
	}
	r := []rune(t)
	for cut := len(r) - 1; cut > 0; cut-- {
		digits := true
		for _, c := range r[cut:] {
			if !unicode.IsDigit(c) {
				digits = false
				break
			}
		}
		if digits {
			if base, _ := idx.base(string(r[:cut])); base != "" {
				return base + string(r[cut:]), true
			}
		}
	}
	return t, false
}
func replaceIdentifiers(text string, index *replacementIndex) (string, int, int) {
	var b strings.Builder
	changed, derived := 0, 0
	for _, t := range lex(text) {
		v := t.text
		if t.kind == "identifier" {
			bare := strings.TrimPrefix(v, "@")
			r, d := index.resolve(bare)
			if r != bare {
				if strings.HasPrefix(v, "@") {
					r = "@" + r
				}
				v = r
				changed++
				if d {
					derived++
				}
			}
		}
		b.WriteString(v)
	}
	return b.String(), changed, derived
}
func managedMirror(output string) bool {
	b, e := os.ReadFile(filepath.Join(output, mirrorManifest))
	if e != nil {
		return false
	}
	var m struct {
		Tool string `json:"tool"`
	}
	return json.Unmarshal(b, &m) == nil && m.Tool == mirrorTool
}

// RunDeobfuscate parses the source mirror command's standalone arguments.
func RunDeobfuscate(args []string, out io.Writer) error {
	f := flag.NewFlagSet("deobfuscate", flag.ContinueOnError)
	f.SetOutput(out)
	source := f.String("source", "", "client Assembly-CSharp directory")
	mapping := f.String("mapping", "", "ObfuscationTranslation file")
	output := f.String("output", "", "mirror directory outside --source")
	if e := f.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if f.NArg() != 0 {
		return errors.New("deobfuscate does not accept positional arguments")
	}
	if *source == "" || *mapping == "" || *output == "" {
		return errors.New("--source, --mapping and --output are required")
	}
	m, e := deobfuscate(*source, *mapping, *output)
	if e != nil {
		return e
	}
	stats := m["statistics"].(map[string]int)
	_, e = fmt.Fprintf(out, "wrote %s (%d files, %d replacements)\n", m["output"], stats["files"], stats["identifier_replacements"])
	return e
}
func deobfuscate(source, mapping, output string) (_ map[string]any, retErr error) {
	source, output, e := safeTrees(source, output)
	if e != nil {
		return nil, e
	}
	mapping, e = canonicalPath(mapping)
	if e != nil {
		return nil, e
	}
	if within(mapping, output) {
		return nil, errors.New("mapping must be outside output")
	}
	entries, warnings, e := parseMapping(mapping)
	if e != nil {
		return nil, e
	}
	replacements, report, notes := buildReplacements(entries)
	warnings = append(warnings, notes...)
	index := newReplacementIndex(replacements)
	for k, s := range index.derived {
		if len(s) > 1 {
			warnings = append(warnings, fmt.Sprintf("derived name %q: ignored ambiguous camel-case sources (%d candidates)", k, len(s)))
		}
	}
	if _, e = os.Lstat(output); e == nil && !managedMirror(output) {
		return nil, fmt.Errorf("refusing to overwrite non-managed output directory: %s", output)
	} else if e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	if e = os.MkdirAll(filepath.Dir(output), 0755); e != nil {
		return nil, e
	}
	stage, e := os.MkdirTemp(filepath.Dir(output), ".bd2-deobfuscate-stage-")
	if e != nil {
		return nil, e
	}
	defer func() { retErr = errors.Join(retErr, os.RemoveAll(stage)) }()
	paths := []string{}
	e = filepath.WalkDir(source, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("source contains symbolic link: %s", p)
		}
		if !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	sort.Strings(paths)
	files := []map[string]any{}
	occupied := map[string]bool{}
	stats := map[string]int{"mapping_entries": len(report), "files": len(paths), "csharp_files": 0, "identifier_replacements": 0, "derived_identifier_replacements": 0}
	for _, p := range paths {
		rel, _ := filepath.Rel(source, p)
		dest := rel
		ext := filepath.Ext(rel)
		if strings.EqualFold(ext, ".cs") {
			stem := strings.TrimSuffix(filepath.Base(rel), ext)
			if r, ok := replacements[stem]; ok {
				stem = r
			}
			dest = filepath.Join(filepath.Dir(rel), stem+ext)
			for n := 2; occupied[strings.ToLower(dest)]; n++ {
				dest = filepath.Join(filepath.Dir(rel), fmt.Sprintf("%s__file_%d%s", stem, n, ext))
			}
		}
		occupied[strings.ToLower(dest)] = true
		target := filepath.Join(stage, dest)
		if e = os.MkdirAll(filepath.Dir(target), 0755); e != nil {
			return nil, e
		}
		record := map[string]any{"source": filepath.ToSlash(rel), "output": filepath.ToSlash(dest), "action": "copied"}
		if strings.EqualFold(ext, ".cs") {
			stats["csharp_files"]++
			text, enc, err := readSource(p)
			if err != nil {
				if _, err = os.Stat(p); err != nil {
					return nil, err
				}
				e = copyFile(p, target)
				record["reason"] = "unsupported text encoding"
				warnings = append(warnings, rel+": copied without replacement (unsupported encoding)")
			} else {
				translated, changed, derived := replaceIdentifiers(text, index)
				e = writeSource(target, translated, enc)
				record["action"] = "translated"
				record["identifier_replacements"] = changed
				record["derived_identifier_replacements"] = derived
				stats["identifier_replacements"] += changed
				stats["derived_identifier_replacements"] += derived
			}
		} else {
			e = copyFile(p, target)
		}
		if e != nil {
			return nil, e
		}
		files = append(files, record)
	}
	manifest := map[string]any{"tool": mirrorTool, "source": source, "output": output, "mapping": mapping, "mapping_entries": report, "files": files, "warnings": warnings, "statistics": stats, "limitations": []string{"Only C# identifier tokens are changed; comments and literal contents are preserved.", "Qualified mapping values become their terminal identifier component.", "Decompiler-derived suffix and camel-case locals are resolved only when unambiguous.", "This is a searchable mirror, not a promise that the transformed source compiles."}}
	b, e := json.MarshalIndent(manifest, "", "  ")
	if e != nil {
		return nil, e
	}
	if e = os.WriteFile(filepath.Join(stage, mirrorManifest), append(b, '\n'), 0644); e != nil {
		return nil, e
	}
	if _, e = os.Stat(output); e == nil {
		if !managedMirror(output) {
			return nil, errors.New("output ownership changed during generation")
		}
		backup, err := os.MkdirTemp(filepath.Dir(output), ".bd2-deobfuscate-backup-")
		if err != nil {
			return nil, err
		}
		if err = os.Remove(backup); err != nil {
			return nil, err
		}
		if err = os.Rename(output, backup); err != nil {
			return nil, err
		}
		if err = os.Rename(stage, output); err != nil {
			restore := os.Rename(backup, output)
			return nil, errors.Join(err, restore)
		}
		if err = os.RemoveAll(backup); err != nil {
			return nil, err
		}
	} else if os.IsNotExist(e) {
		if e = os.Rename(stage, output); e != nil {
			return nil, e
		}
	} else {
		return nil, e
	}
	return manifest, nil
}
