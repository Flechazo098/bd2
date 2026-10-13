package sourcetools

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var suffixPattern = regexp.MustCompile(`__from_u[0-9a-fA-F]{4}(?:_u[0-9a-fA-F]{4})*`)
var nonType = wordSet("return throw new yield case goto break continue else if while switch lock nameof typeof sizeof default await this base true false null in is as do try finally checked unchecked")
var modifiers = wordSet("public private protected internal static virtual override sealed abstract partial readonly volatile const async unsafe extern new ref out in params event")

func contains(s []string, t string) bool { return slices.Contains(s, t) }
func tokenize(text string, symbols map[string]string) []string {
	r := []string{}
	for _, t := range lex(text) {
		if t.kind == "comment" || t.kind == "space" {
			continue
		}
		v := t.text
		if t.kind == "identifier" {
			if mapped, ok := symbols[v]; ok {
				v = mapped
			} else {
				v = suffixPattern.ReplaceAllString(v, "")
			}
		}
		r = append(r, v)
	}
	out := []string{}
	for i, t := range r {
		if t == "global" && i+1 < len(r) && r[i+1] == "::" || t == "::" && i > 0 && r[i-1] == "global" {
			continue
		}
		out = append(out, t)
	}
	return out
}
func pairs(s []string) map[int]int {
	stacks := map[string][]int{"(": {}, "[": {}, "{": {}}
	closing := map[string]string{")": "(", "]": "[", "}": "{"}
	r := map[int]int{}
	for i, t := range s {
		if stack, ok := stacks[t]; ok {
			stacks[t] = append(stack, i)
		} else if open, ok := closing[t]; ok {
			stack := stacks[open]
			if len(stack) > 0 {
				r[stack[len(stack)-1]] = i
				stacks[open] = stack[:len(stack)-1]
			}
		}
	}
	return r
}
func withoutAttributes(s []string) []string {
	ends := pairs(s)
	i := 0
	for i < len(s) && s[i] == "[" {
		j, ok := ends[i]
		if !ok {
			break
		}
		i = j + 1
	}
	return s[i:]
}
func assignmentAt(s []string) int {
	ends := pairs(s)
	for i := 0; i < len(s); {
		if j, ok := ends[i]; ok && (s[i] == "(" || s[i] == "[") {
			i = j + 1
		} else if s[i] == "=" || s[i] == "=>" {
			return i
		} else {
			i++
		}
	}
	return len(s)
}
func memberKey(s []string) string {
	s = withoutAttributes(s)
	s = s[:assignmentAt(s)]
	paren := slices.Index(s, "(")
	if paren >= 0 {
		begin := paren - 1
		if begin >= 0 && s[begin] == ">" {
			depth := 1
			begin--
			for begin >= 0 && depth > 0 {
				if s[begin] == ">" {
					depth++
				}
				if s[begin] == "<" {
					depth--
				}
				begin--
			}
		}
		end, ok := pairs(s)[paren]
		if !ok {
			end = len(s) - 1
		}
		parameters := []string{}
		current := []string{}
		depth := 0
		for _, t := range append(slices.Clone(s[paren+1:end]), ",") {
			if t == "," && depth == 0 {
				part := current
				if eq := slices.Index(part, "="); eq >= 0 {
					part = part[:eq]
				}
				if len(part) > 1 && identifier(part[len(part)-1]) {
					part = part[:len(part)-1]
				}
				parameters = append(parameters, strings.Join(part, " "))
				current = nil
			} else {
				current = append(current, t)
				if contains([]string{"<", "[", "("}, t) {
					depth++
				}
				if contains([]string{">", "]", ")"}, t) {
					depth--
				}
			}
		}
		name := strings.Join(s[max(0, begin):paren], " ")
		if begin > 0 && s[begin-1] == "~" {
			name = "~" + name
		}
		return name + "(" + strings.Join(parameters, ", ") + ")"
	}
	r := []string{}
	for _, t := range s {
		if !modifiers[t] {
			r = append(r, t)
		}
	}
	return strings.Join(r, " ")
}

type member struct {
	key       string
	tokens    []string
	body      int
	kind      string
	ambiguous bool
}

func splitMembers(tokens []string) []*member {
	ends := pairs(tokens)
	result := []*member{}
	counts := map[string]int{}
	emit := func(key string, seq []string, body int, kind string) {
		counts[key]++
		result = append(result, &member{key: fmt.Sprintf("%s #%d", key, counts[key]), tokens: seq, body: body, kind: kind})
	}
	var scope func(int, int, string)
	scope = func(start, stop int, prefix string) {
		for i := start; i < stop; {
			begin := i
			for i < stop {
				t := tokens[i]
				if j, ok := ends[i]; ok && (t == "[" || t == "(") {
					i = j + 1
				} else if t == ";" || t == "{" {
					break
				} else {
					i++
				}
			}
			if i == stop {
				if begin < stop {
					emit(prefix+" / trailing", tokens[begin:stop], -1, "member")
				}
				return
			}
			header := tokens[begin:i]
			plain := withoutAttributes(header)
			first := 0
			for first < len(plain) && modifiers[plain[first]] {
				first++
			}
			typeAt := -1
			if first < len(plain) && contains([]string{"namespace", "class", "struct", "interface", "enum"}, plain[first]) {
				typeAt = first
			}
			end, hasEnd := ends[i]
			if tokens[i] == "{" && hasEnd && typeAt >= 0 {
				tail := plain[typeAt+1:]
				if colon := slices.Index(tail, ":"); colon >= 0 {
					tail = tail[:colon]
				}
				path := prefix + " / " + strings.Join(tail, " ")
				if plain[typeAt] == "enum" {
					emit(path, tokens[begin:end+1], -1, "enum")
				} else {
					emit(path+" / declaration", header, -1, "type")
					scope(i+1, end, path)
				}
				i = end + 1
				continue
			}
			body := -1
			if tokens[i] == "{" {
				if !hasEnd {
					emit(prefix+" / unparsed", tokens[begin:stop], -1, "member")
					return
				}
				body = i - begin
				i = end + 1
				if assignmentAt(header) < len(header) {
					for i < stop && tokens[i] != ";" {
						if j, ok := ends[i]; ok && contains([]string{"{", "[", "("}, tokens[i]) {
							i = j + 1
						} else {
							i++
						}
					}
					if i < stop {
						i++
					}
					body = -1
				} else if i < stop && tokens[i] == ";" {
					i++
				}
			} else {
				i++
			}
			kind := "member"
			if body >= 0 && contains(plain[:assignmentAt(plain)], "(") {
				kind = "method"
			}
			if prefix == "file" && len(plain) > 0 && plain[0] == "using" {
				kind = "using"
			}
			emit(prefix+" / "+memberKey(header), tokens[begin:i], body, kind)
		}
	}
	scope(0, len(tokens), "file")
	return result
}
func typeEnd(s []string, i int) int {
	if i >= len(s) || !identifier(s[i]) || nonType[s[i]] {
		return i
	}
	j := i + 1
	for j < len(s) {
		if (s[j] == "." || s[j] == "::") && j+1 < len(s) && identifier(s[j+1]) {
			j += 2
		} else if s[j] == "<" {
			depth, k := 1, j+1
			for k < len(s) && depth > 0 {
				if s[k] == "<" {
					depth++
				} else if s[k] == ">" {
					depth--
				} else if !identifier(s[k]) && !contains([]string{",", ".", "[", "]", "?", "::"}, s[k]) {
					return i
				}
				k++
			}
			if depth > 0 {
				return i
			}
			j = k
		} else if s[j] == "[" {
			k := j + 1
			for k < len(s) && s[k] == "," {
				k++
			}
			if k >= len(s) || s[k] != "]" {
				break
			}
			j = k + 1
		} else if s[j] == "?" {
			j++
		} else {
			break
		}
	}
	return j
}
func alphaLocals(m *member) []string {
	seq := slices.Clone(m.tokens)
	if m.body < 0 || m.kind != "method" || contains(seq, "nameof") {
		return seq
	}
	scopes := []map[string]string{{}}
	scopeEnds := []int{len(seq)}
	ends := pairs(seq)
	declarations := map[int]string{}
	typePositions := map[int]bool{}
	parameters := map[string]bool{}
	header := withoutAttributes(seq[:m.body])
	if begin := slices.Index(header, "("); begin >= 0 {
		end := pairs(header)[begin]
		part := []string{}
		depth := 0
		for _, t := range append(slices.Clone(header[begin+1:end]), ",") {
			if t == "," && depth == 0 {
				p := part
				if eq := slices.Index(p, "="); eq >= 0 {
					p = p[:eq]
				}
				if len(p) > 0 && identifier(p[len(p)-1]) {
					parameters[p[len(p)-1]] = true
				}
				part = nil
			} else {
				part = append(part, t)
				if contains([]string{"<", "[", "("}, t) {
					depth++
				}
				if contains([]string{">", "]", ")"}, t) {
					depth--
				}
			}
		}
	}
	mark := func(start, end int) {
		for p := start; p < end; p++ {
			typePositions[p] = true
		}
	}
	for i := 0; i+1 < len(seq); i++ {
		if contains([]string{"new", "as", "is"}, seq[i]) {
			mark(i+1, typeEnd(seq, i+1))
		} else if contains([]string{"typeof", "sizeof", "default"}, seq[i]) && seq[i+1] == "(" {
			mark(i+2, typeEnd(seq, i+2))
		} else if seq[i] == "<" && i > 0 {
			if end := typeEnd(seq, i-1); end > i+1 {
				mark(i+1, end)
			}
		}
	}
	serial := 0
	for i := m.body + 1; i < len(seq); i++ {
		for len(scopes) > 1 && i > scopeEnds[len(scopeEnds)-1] {
			scopes = scopes[:len(scopes)-1]
			scopeEnds = scopeEnds[:len(scopeEnds)-1]
		}
		if seq[i] == "{" {
			scopes = append(scopes, map[string]string{})
			scopeEnds = append(scopeEnds, ends[i])
		} else if contains([]string{"for", "foreach", "using", "catch"}, seq[i]) && i+1 < len(seq) && seq[i+1] == "(" {
			if headerEnd, ok := ends[i+1]; ok && headerEnd+1 < len(seq) {
				end := headerEnd + 1
				if blockEnd, ok := ends[end]; ok && seq[end] == "{" {
					end = blockEnd
				} else {
					for end < len(seq)-1 && seq[end] != ";" {
						end++
					}
				}
				scopes = append(scopes, map[string]string{})
				scopeEnds = append(scopeEnds, end)
			}
		}
		prev := m.tokens[i-1]
		boundary := contains([]string{";", "{", "}"}, prev) || prev == "(" && i > 1 && contains([]string{"for", "foreach", "using", "catch"}, seq[i-2]) || prev == "out"
		if boundary {
			j := typeEnd(m.tokens, i)
			if j > i && j+1 < len(seq) && identifier(seq[j]) && contains([]string{"=", ";", ",", "in", ")"}, seq[j+1]) {
				serial++
				name := m.tokens[j]
				binding := fmt.Sprintf("local%d", serial)
				if parameters[name] || scopes[len(scopes)-1][name] != "" && contains([]string{";", "{", "}"}, prev) {
					m.ambiguous = true
				}
				scopes[len(scopes)-1][name] = binding
				declarations[j] = binding
				mark(i, j)
			}
		}
		if binding, ok := declarations[i]; ok {
			seq[i] = binding
		} else if !typePositions[i] && identifier(seq[i]) && !contains([]string{".", "?.", "::"}, prev) {
			for j := range slices.Backward(scopes) {
				if binding, ok := scopes[j][seq[i]]; ok {
					seq[i] = binding
					break
				}
			}
		}
	}
	return seq
}

var generatedPatterns = []*regexp.Regexp{regexp.MustCompile(`c__DisplayClass[0-9]+_[0-9]+`), regexp.MustCompile(`b__[0-9]+_`), regexp.MustCompile(`d__[0-9]+`), regexp.MustCompile(`9__[0-9]+_`), regexp.MustCompile(`g__\w+?_[0-9]+_[0-9]+`)}

func generated(s string) string {
	for n, r := range generatedPatterns {
		s = r.ReplaceAllStringFunc(s, func(t string) string {
			switch n {
			case 0:
				i := strings.LastIndex(t, "_")
				return "c__DisplayClassN" + t[i:]
			case 1:
				return "b__N_"
			case 2:
				return "d__N"
			case 3:
				return "9__N_"
			default:
				i := strings.LastIndex(t, "_")
				j := strings.LastIndex(t[:i], "_")
				return t[:j+1] + "N" + t[i:]
			}
		})
	}
	return s
}
func outerParentheses(seq []string) []string {
	ends := pairs(seq)
	drop := map[int]bool{}
	precedence := map[string]int{"??": 1, "||": 2, "&&": 3, "|": 4, "^": 5, "&": 6, "==": 7, "!=": 7, "<": 8, ">": 8, "<=": 8, ">=": 8, "is": 8, "as": 8, "+": 10, "-": 10, "*": 11, "/": 11, "%": 11}
	for i, j := range ends {
		if seq[i] != "(" || i == 0 {
			continue
		}
		prev := seq[i-1]
		following := ""
		if j+1 < len(seq) {
			following = seq[j+1]
		}
		doubled := i+1 < j && seq[i+1] == "(" && ends[i+1] == j-1
		whole := contains([]string{"return", "=", "+=", "-=", "*=", "/="}, prev) && following == ";"
		if whole {
			for k := i + 1; k < j; {
				if end, ok := ends[k]; ok && contains([]string{"(", "[", "{"}, seq[k]) {
					k = end + 1
				} else if seq[k] == "," {
					whole = false
					break
				} else {
					k++
				}
			}
		}
		redundant := false
		_, prevOp := precedence[prev]
		_, nextOp := precedence[following]
		if i+1 < j && prev != "<" && prev != ">" && (prevOp || contains([]string{"(", "[", ",", "=", "return", "?", ":", "!", "~"}, prev)) && (nextOp || contains([]string{";", ",", ")", "]", "?", ":", "}"}, following)) {
			minimum := 17
			for k := i + 1; k < j; {
				t := seq[k]
				if end, ok := ends[k]; ok && (t == "(" || t == "[") {
					k = end + 1
					continue
				}
				if contains([]string{"=", "=>", "?", ":", ";", ",", "++", "--"}, t) {
					minimum = 0
					break
				}
				if (t == "<" || t == ">") && k+1 < j && seq[k+1] == t {
					minimum = min(minimum, 9)
					k += 2
					continue
				}
				if p, ok := precedence[t]; ok {
					_, priorOp := precedence[seq[k-1]]
					if !contains([]string{"+", "-"}, t) || k != i+1 && !priorOp {
						minimum = min(minimum, p)
					}
				}
				k++
			}
			redundant = minimum > max(precedence[prev], precedence[following])
		}
		if doubled || whole || redundant {
			drop[i], drop[j] = true, true
		}
	}
	result := []string{}
	for i, t := range seq {
		if !drop[i] {
			result = append(result, t)
		}
	}
	return result
}

type typeargs struct {
	start, end int
	name       string
}

func callTypeargs(seq []string, start int) []typeargs {
	r := []typeargs{}
	for i := start; i < len(seq); i++ {
		if seq[i] != "<" || i == 0 || !identifier(seq[i-1]) {
			continue
		}
		depth, j := 1, i+1
		for j < len(seq) && depth > 0 {
			t := seq[j]
			if t == "<" {
				depth++
			} else if t == ">" {
				depth--
			} else if !identifier(t) && !contains([]string{",", ".", "[", "]", "?", "::"}, t) {
				break
			}
			j++
		}
		k := i - 2
		for k >= 1 && (seq[k] == "." || seq[k] == "::") {
			k -= 2
		}
		constructor := k >= 0 && seq[k] == "new"
		if depth == 0 && j < len(seq) && seq[j] == "(" && !constructor {
			r = append(r, typeargs{i, j, seq[i-1]})
			i = j
		}
	}
	return r
}
func inferredGenerics(seq []string, start int, allowed map[string]bool) []string {
	drop := map[int]bool{}
	for _, c := range callTypeargs(seq, start) {
		if allowed[c.name] {
			for i := c.start; i < c.end; i++ {
				drop[i] = true
			}
		}
	}
	r := []string{}
	for i, t := range seq {
		if !drop[i] {
			r = append(r, t)
		}
	}
	return r
}

var labelPattern = regexp.MustCompile(`^IL_[0-9A-Fa-f]+$`)

func heuristic(m *member) []string {
	seq := alphaLocals(m)
	for i, t := range seq {
		if identifier(t) {
			seq[i] = generated(t)
		}
	}
	for i := 0; i < len(seq); i++ {
		if seq[i] != "<" || i == 0 || !identifier(seq[i-1]) {
			continue
		}
		depth, j := 1, i+1
		for j < len(seq) && depth > 0 {
			if seq[j] == "<" {
				depth++
			}
			if seq[j] == ">" {
				depth--
			}
			j++
		}
		if depth == 0 && j+1 < len(seq) && seq[j] == "." && strings.HasPrefix(seq[j+1], "c__DisplayClass") {
			chunk := append([]string{".", seq[j+1]}, seq[i:j]...)
			copy(seq[i:j+2], chunk)
			i = j + 1
		}
	}
	labels := map[string]string{}
	for i, t := range seq {
		if i+1 < len(seq) && labelPattern.MatchString(t) && seq[i+1] == ":" {
			if _, ok := labels[t]; !ok {
				labels[t] = fmt.Sprintf("label%d", len(labels)+1)
			}
		}
	}
	for i, t := range seq {
		if label, ok := labels[t]; ok {
			seq[i] = label
		}
	}
	ends := pairs(seq)
	attrs := [][]string{}
	i := 0
	for i < len(seq) && seq[i] == "[" {
		end, ok := ends[i]
		if !ok {
			break
		}
		attrs = append(attrs, seq[i:end+1])
		i = end + 1
	}
	if len(attrs) > 1 {
		sort.Slice(attrs, func(i, j int) bool { return strings.Join(attrs[i], "\x00") < strings.Join(attrs[j], "\x00") })
		r := []string{}
		for _, a := range attrs {
			r = append(r, a...)
		}
		seq = append(r, seq[i:]...)
	}
	return outerParentheses(seq)
}
func normalizePair(a, b *member) ([]string, []string) {
	ca, cb := []string{}, []string{}
	if a != nil {
		ca = heuristic(a)
	}
	if b != nil {
		cb = heuristic(b)
	}
	if a != nil && b != nil && a.kind == "method" && b.kind == "method" {
		startA, startB := a.body+1, b.body+1
		namesA, namesB := map[string]bool{}, map[string]bool{}
		for _, c := range callTypeargs(ca, startA) {
			namesA[c.name] = true
		}
		for _, c := range callTypeargs(cb, startB) {
			namesB[c.name] = true
		}
		bare := func(s []string, start int) map[string]bool {
			r := map[string]bool{}
			for i := start; i < len(s); i++ {
				if s[i] == "(" && i > 0 && identifier(s[i-1]) {
					r[s[i-1]] = true
				}
			}
			return r
		}
		bareA, bareB := bare(ca, startA), bare(cb, startB)
		allowedA, allowedB := map[string]bool{}, map[string]bool{}
		for n := range namesA {
			allowedA[n] = !namesB[n] && bareB[n]
		}
		for n := range namesB {
			allowedB[n] = !namesA[n] && bareA[n]
		}
		ca = inferredGenerics(ca, startA, allowedA)
		cb = inferredGenerics(cb, startB, allowedB)
	}
	return ca, cb
}
