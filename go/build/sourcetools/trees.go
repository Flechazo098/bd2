package sourcetools

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Python-style globs match slashes as ordinary characters.
func glob(pattern, value string) bool {
	var b strings.Builder
	b.WriteByte('^')
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteByte('.')
		case '[':
			j := i + 1
			for j < len(pattern) && pattern[j] != ']' {
				j++
			}
			if j < len(pattern) {
				class := pattern[i+1 : j]
				if strings.HasPrefix(class, "!") {
					class = "^" + class[1:]
				}
				b.WriteByte('[')
				b.WriteString(class)
				b.WriteByte(']')
				i = j
			} else {
				b.WriteString(`\[`)
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteByte('$')
	r, e := regexp.Compile(b.String())
	return e == nil && r.MatchString(value)
}
func inventory(root string, includes, excludes []string) ([]string, error) {
	found := []string{}
	skip := wordSet("bin obj .git .vs __pycache__")
	e := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("source contains symbolic link: %s", p)
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(p), ".cs") {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		logical := suffixPattern.ReplaceAllString(rel, "")
		included := len(includes) == 0
		for _, pat := range includes {
			included = included || glob(pat, logical)
		}
		if !included {
			return nil
		}
		for _, pat := range excludes {
			if glob(pat, logical) {
				return nil
			}
		}
		found = append(found, rel)
		return nil
	})
	sort.Strings(found)
	return found, e
}

type fileTask struct{ logical, old, new string }

func pairTrees(a, b string, ia, ib []string) ([]fileTask, map[string]string, map[string]string, []string) {
	ga, gb := map[string][]string{}, map[string][]string{}
	for _, p := range ia {
		logical := suffixPattern.ReplaceAllString(p, "")
		ga[logical] = append(ga[logical], p)
	}
	for _, p := range ib {
		logical := suffixPattern.ReplaceAllString(p, "")
		gb[logical] = append(gb[logical], p)
	}
	tasks := []fileTask{}
	symbolsA, symbolsB := map[string]string{}, map[string]string{}
	warnings := []string{}
	appendTask := func(logical, old, new string) {
		tasks = append(tasks, fileTask{logical, old, new})
		name := strings.TrimSuffix(filepath.Base(logical), filepath.Ext(logical))
		for i, p := range []string{old, new} {
			stem := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
			if p != "" && suffixPattern.MatchString(stem) {
				symbols := symbolsA
				if i == 1 {
					symbols = symbolsB
				}
				symbols[stem] = name
			}
		}
	}
	shapeCache := map[string]map[string]bool{}
	shape := func(root, rel string) map[string]bool {
		cacheKey := filepath.Join(root, rel)
		if s, ok := shapeCache[cacheKey]; ok {
			return s
		}
		text, _, e := readSource(cacheKey)
		if e != nil {
			shapeCache[cacheKey] = nil
			return nil
		}
		s := map[string]bool{}
		for _, m := range splitMembers(tokenize(text, nil)) {
			s[generated(m.key)] = true
		}
		shapeCache[cacheKey] = s
		return s
	}
	fingerprint := func(s map[string]bool) string {
		if s == nil {
			return ""
		}
		keys := []string{}
		for k := range s {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return strings.Join(keys, "\x00")
	}
	logicals := map[string]bool{}
	for k := range ga {
		logicals[k] = true
	}
	for k := range gb {
		logicals[k] = true
	}
	ordered := []string{}
	for k := range logicals {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)
	sortedSet := func(s map[string]bool) []string {
		r := []string{}
		for k := range s {
			r = append(r, k)
		}
		sort.Strings(r)
		return r
	}
	for _, logical := range ordered {
		left, right := map[string]bool{}, map[string]bool{}
		for _, p := range ga[logical] {
			left[p] = true
		}
		for _, p := range gb[logical] {
			right[p] = true
		}
		collision := len(left) > 1 || len(right) > 1
		for _, p := range sortedSet(left) {
			if right[p] {
				key := logical
				if collision {
					key = p
				}
				appendTask(key, p, p)
				delete(left, p)
				delete(right, p)
			}
		}
		if !collision {
			if len(left)+len(right) > 0 {
				old, new := "", ""
				for p := range left {
					old = p
				}
				for p := range right {
					new = p
				}
				appendTask(logical, old, new)
			}
			continue
		}
		sa, sb := map[string][]string{}, map[string][]string{}
		for _, p := range sortedSet(left) {
			key := fingerprint(shape(a, p))
			sa[key] = append(sa[key], p)
		}
		for _, p := range sortedSet(right) {
			key := fingerprint(shape(b, p))
			sb[key] = append(sb[key], p)
		}
		variant := 0
		variantKey := func() string {
			variant++
			ext := filepath.Ext(logical)
			return strings.TrimSuffix(logical, ext) + fmt.Sprintf("__variant_%d", variant) + ext
		}
		fps := []string{}
		for key := range sa {
			if key != "" && len(sa[key]) == 1 && len(sb[key]) == 1 {
				fps = append(fps, key)
			}
		}
		sort.Strings(fps)
		for _, key := range fps {
			old, new := sa[key][0], sb[key][0]
			appendTask(variantKey(), old, new)
			delete(left, old)
			delete(right, new)
		}
		type candidate struct {
			score    float64
			old, new string
		}
		candidates := []candidate{}
		for _, old := range sortedSet(left) {
			fa := shape(a, old)
			for _, new := range sortedSet(right) {
				fb := shape(b, new)
				if len(fa) == 0 || len(fb) == 0 {
					continue
				}
				inter := 0
				for k := range fa {
					if fb[k] {
						inter++
					}
				}
				score := float64(inter) / float64(len(fa)+len(fb)-inter)
				if score >= .85 {
					candidates = append(candidates, candidate{score, old, new})
				}
			}
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].score != candidates[j].score {
				return candidates[i].score > candidates[j].score
			}
			if candidates[i].old != candidates[j].old {
				return candidates[i].old > candidates[j].old
			}
			return candidates[i].new > candidates[j].new
		})
		for _, c := range candidates {
			if !left[c.old] || !right[c.new] {
				continue
			}
			rival := float64(-1)
			for _, other := range candidates {
				if (other.old == c.old || other.new == c.new) && (other.old != c.old || other.new != c.new) {
					rival = max(rival, other.score)
				}
			}
			if rival >= 0 && c.score-rival < .15 {
				continue
			}
			appendTask(variantKey(), c.old, c.new)
			delete(left, c.old)
			delete(right, c.new)
		}
		if len(left) == 1 && len(right) == 1 {
			old, new := sortedSet(left)[0], sortedSet(right)[0]
			appendTask(variantKey(), old, new)
			delete(left, old)
			delete(right, new)
		}
		for i, remainder := range []map[string]bool{left, right} {
			side := "old"
			if i == 1 {
				side = "new"
			}
			for _, p := range sortedSet(remainder) {
				warnings = append(warnings, fmt.Sprintf("同名类型未能唯一配对，保留为%s独立文件: %s", side, p))
				tag := sha256.Sum256([]byte(p))
				ext := filepath.Ext(logical)
				key := strings.TrimSuffix(logical, ext) + fmt.Sprintf("__unpaired_%s_%x", side, tag[:6]) + ext
				old, new := p, ""
				if i == 1 {
					old, new = "", p
				}
				appendTask(key, old, new)
			}
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].logical < tasks[j].logical })
	return tasks, symbolsA, symbolsB, warnings
}
func compareFile(a, b string, t fileTask, context int, strict bool, symbolsA, symbolsB map[string]string) fileResult {
	result := fileResult{Path: t.logical, Status: "unchanged", Members: []diffEntry{}, Noise: []diffEntry{}}
	if t.old != "" {
		p := t.old
		result.OldPath = &p
	}
	if t.new != "" {
		p := t.new
		result.NewPath = &p
	}
	old, new := "", ""
	var err error
	if t.old != "" {
		old, _, err = readSource(filepath.Join(a, t.old))
	}
	if err == nil && t.new != "" {
		new, _, err = readSource(filepath.Join(b, t.new))
	}
	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
		return result
	}
	if t.old == "" || t.new == "" {
		result.Status = "added"
		oldName, newName := t.old, t.new
		if t.new == "" {
			result.Status = "removed"
			newName = "/dev/null"
		} else {
			oldName = "/dev/null"
		}
		result.Members = append(result.Members, diffEntry{Name: "整个文件", Kind: result.Status, Diff: unified(render(tokenize(old, symbolsA)), render(tokenize(new, symbolsB)), oldName, newName, context)})
		return result
	}
	compared := compareText(old, new, context, strict, symbolsA, symbolsB)
	compared.Path, compared.OldPath, compared.NewPath = result.Path, result.OldPath, result.NewPath
	return compared
}
