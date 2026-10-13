package sourcetools

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

type diffEntry struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Diff       string `json:"diff"`
	SourceDiff string `json:"source_diff,omitempty"`
	Reason     string `json:"reason,omitempty"`
}
type fileResult struct {
	Path    string      `json:"path"`
	OldPath *string     `json:"old_path"`
	NewPath *string     `json:"new_path"`
	Status  string      `json:"status"`
	Members []diffEntry `json:"members"`
	Noise   []diffEntry `json:"noise"`
	Review  []diffEntry `json:"review,omitempty"`
	Error   string      `json:"error,omitempty"`
}

func formatTokens(s []string) string {
	var b strings.Builder
	previous := ""
	for _, t := range s {
		compact := contains([]string{".", "?.", "::", ",", ";", ")", "]"}, t) || contains([]string{".", "?.", "::", "(", "["}, previous) || t == previous && (t == "<" || t == ">")
		if t == "(" && previous != "" && (identifier(previous) || contains([]string{")", "]", ">"}, previous)) && !contains([]string{"if", "while", "switch", "for", "foreach", "using", "catch", "lock"}, previous) {
			compact = true
		}
		if b.Len() > 0 && !compact {
			b.WriteByte(' ')
		}
		b.WriteString(t)
		previous = t
	}
	return b.String()
}
func render(seq []string) []string {
	lines := []string{}
	current := []string{}
	parens, indent := 0, 0
	flush := func() {
		if len(current) > 0 {
			lines = append(lines, strings.Repeat("    ", max(0, indent))+formatTokens(current))
			current = nil
		}
	}
	for _, t := range seq {
		if t == "}" {
			flush()
			indent--
		}
		current = append(current, t)
		switch t {
		case "(":
			parens++
		case ")":
			parens--
		}
		if t == "{" {
			flush()
			indent++
		} else if t == "}" || t == ";" && parens == 0 {
			flush()
		}
	}
	flush()
	return lines
}

type editLine struct {
	kind byte
	line string
}

func lineEdits(a, b []string) []editLine {
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	result := []editLine{}
	for _, l := range a[:prefix] {
		result = append(result, editLine{' ', l})
	}
	left, right := a[prefix:len(a)-suffix], b[prefix:len(b)-suffix]
	if len(left)*len(right) <= 4_000_000 {
		width := len(right) + 1
		dp := make([]int, (len(left)+1)*width)
		for i := range slices.Backward(left) {
			for j := range slices.Backward(right) {
				if left[i] == right[j] {
					dp[i*width+j] = 1 + dp[(i+1)*width+j+1]
				} else {
					dp[i*width+j] = max(dp[(i+1)*width+j], dp[i*width+j+1])
				}
			}
		}
		i, j := 0, 0
		for i < len(left) || j < len(right) {
			if i < len(left) && j < len(right) && left[i] == right[j] {
				result = append(result, editLine{' ', left[i]})
				i++
				j++
			} else if i < len(left) && (j == len(right) || dp[(i+1)*width+j] >= dp[i*width+j+1]) {
				result = append(result, editLine{'-', left[i]})
				i++
			} else {
				result = append(result, editLine{'+', right[j]})
				j++
			}
		}
	} else {
		for _, l := range left {
			result = append(result, editLine{'-', l})
		}
		for _, l := range right {
			result = append(result, editLine{'+', l})
		}
	}
	for _, l := range a[len(a)-suffix:] {
		result = append(result, editLine{' ', l})
	}
	return result
}
func unified(a, b []string, old, new string, context int) string {
	if slices.Equal(a, b) {
		return "\n"
	}
	edits := lineEdits(a, b)
	type hunk struct{ start, end int }
	hunks := []hunk{}
	for i, e := range edits {
		if e.kind == ' ' {
			continue
		}
		start, end := max(0, i-context), min(len(edits), i+context+1)
		if len(hunks) > 0 && start <= hunks[len(hunks)-1].end {
			hunks[len(hunks)-1].end = end
		} else {
			hunks = append(hunks, hunk{start, end})
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", old, new)
	oldLine, newLine := 1, 1
	position := 0
	rangeText := func(start, count int) string {
		if count == 0 {
			start--
		}
		if count == 1 {
			return fmt.Sprint(start)
		}
		return fmt.Sprintf("%d,%d", start, count)
	}
	for _, h := range hunks {
		for position < h.start {
			if edits[position].kind != '+' {
				oldLine++
			}
			if edits[position].kind != '-' {
				newLine++
			}
			position++
		}
		oldCount, newCount := 0, 0
		for _, e := range edits[h.start:h.end] {
			if e.kind != '+' {
				oldCount++
			}
			if e.kind != '-' {
				newCount++
			}
		}
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", rangeText(oldLine, oldCount), rangeText(newLine, newCount))
		for position < h.end {
			e := edits[position]
			out.WriteByte(e.kind)
			out.WriteString(e.line)
			out.WriteByte('\n')
			if e.kind != '+' {
				oldLine++
			}
			if e.kind != '-' {
				newLine++
			}
			position++
		}
	}
	return out.String()
}

type indexedMembers struct {
	order   []string
	members map[string]*member
}

func indexMembers(s []*member) indexedMembers {
	r := indexedMembers{[]string{}, map[string]*member{}}
	counts := map[string]int{}
	for _, m := range s {
		base := m.key
		if i := strings.LastIndex(base, " #"); i >= 0 {
			base = base[:i]
		}
		base = generated(base)
		counts[base]++
		key := fmt.Sprintf("%s #%d", base, counts[base])
		r.order = append(r.order, key)
		r.members[key] = m
	}
	return r
}
func memberTokens(m *member) []string {
	if m == nil {
		return nil
	}
	return m.tokens
}
func compareText(old, new string, context int, strict bool, oldSymbols, newSymbols map[string]string) fileResult {
	result := fileResult{Status: "unchanged", Members: []diffEntry{}, Noise: []diffEntry{}}
	a, b := tokenize(old, oldSymbols), tokenize(new, newSymbols)
	if slices.Equal(a, b) {
		return result
	}
	da, db := indexMembers(splitMembers(a)), indexMembers(splitMembers(b))
	keys := []string{}
	all := map[string]bool{}
	for k := range da.members {
		all[k] = true
	}
	for k := range db.members {
		all[k] = true
	}
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	asyncNew, builderOld := false, false
	for _, m := range db.members {
		asyncNew = asyncNew || contains(m.tokens, "async")
	}
	for _, m := range da.members {
		builderOld = builderOld || contains(m.tokens, "t__builder")
	}
	for _, key := range keys {
		left, right := da.members[key], db.members[key]
		if left != nil && right != nil && slices.Equal(left.tokens, right.tokens) {
			continue
		}
		ca, cb := normalizePair(left, right)
		rawA, rawB := render(memberTokens(left)), render(memberTokens(right))
		entry := diffEntry{Name: key, Kind: "modified"}
		if left == nil {
			entry.Kind = "added"
		} else if right == nil {
			entry.Kind = "removed"
		}
		simpleUsing := true
		ambiguous := false
		for _, m := range []*member{left, right} {
			if m != nil {
				simpleUsing = simpleUsing && m.kind == "using" && !contains(m.tokens, "=") && !contains(m.tokens, "static")
				ambiguous = ambiguous || m.ambiguous
			}
		}
		if !strict && !ambiguous && (left != nil && right != nil && slices.Equal(ca, cb) || simpleUsing) {
			entry.Reason = "局部名 / 冗余括号 / 推断泛型 / 生成编号 / 标签 / 属性顺序（启发式，需审阅）"
			entry.Diff = unified(rawA, rawB, "old", "new", context)
			if simpleUsing {
				entry.Reason = "using 声明变化，未做名称绑定证明，保留复核"
				result.Review = append(result.Review, entry)
			} else {
				result.Noise = append(result.Noise, entry)
			}
		} else {
			if strict {
				entry.Diff = unified(rawA, rawB, "old", "new", context)
			} else {
				entry.Diff = unified(render(ca), render(cb), "old", "new", context)
			}
			entry.SourceDiff = unified(rawA, rawB, "old", "new", context)
			structural := false
			if asyncNew && builderOld {
				structural = strings.Contains(key, "_d__") || strings.Contains(key, "_d /")
				for _, m := range []*member{left, right} {
					if m != nil {
						structural = structural || contains(m.tokens, "t__builder") || contains(m.tokens, "async")
					}
				}
			}
			if structural || ambiguous {
				entry.Reason = "async / 状态机导出结构不同，需复核正文"
				if ambiguous {
					entry.Reason = "导出代码含同一作用域重名局部，绑定无法可靠恢复"
				}
				result.Review = append(result.Review, entry)
			} else {
				result.Members = append(result.Members, entry)
			}
		}
	}
	order := func(from, to indexedMembers, methods bool) []string {
		r := []string{}
		for _, key := range from.order {
			m := from.members[key]
			if to.members[key] == nil {
				continue
			}
			include := m.kind == "method" || m.kind == "using"
			if !methods {
				include = !include && m.kind != "type"
			}
			if include {
				r = append(r, key)
			}
		}
		return r
	}
	orderA, orderB := order(da, db, false), order(db, da, false)
	if !slices.Equal(orderA, orderB) {
		result.Members = append(result.Members, diffEntry{Name: "声明 / 初始化顺序", Kind: "order", Diff: unified(orderA, orderB, "old order", "new order", context)})
	}
	methodA, methodB := order(da, db, true), order(db, da, true)
	if !slices.Equal(methodA, methodB) {
		result.Noise = append(result.Noise, diffEntry{Name: "方法 / using 排列顺序", Kind: "order", Reason: "同名成员分别比较，声明顺序单独记录", Diff: unified(methodA, methodB, "old order", "new order", context)})
	}
	if len(result.Members) > 0 {
		result.Status = "modified"
	} else if len(result.Review) > 0 {
		result.Status = "review"
	} else if len(result.Noise) > 0 {
		result.Status = "noise"
	}
	return result
}
