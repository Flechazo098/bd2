package textmoderation

import "unicode/utf8"

type matchNode struct {
	next                map[rune]int
	failure, outputLink int
	outputs             []int
}

type matcher struct {
	nodes []matchNode
	rules []rule
}

func compileMatcher(rules []rule) *matcher {
	m := &matcher{nodes: []matchNode{{}}, rules: rules}
	for index, rule := range rules {
		state := 0
		for _, char := range rule.text {
			next, exists := m.nodes[state].next[char]
			if !exists {
				next = len(m.nodes)
				m.nodes = append(m.nodes, matchNode{})
				if m.nodes[state].next == nil {
					m.nodes[state].next = map[rune]int{}
				}
				m.nodes[state].next[char] = next
			}
			state = next
		}
		m.nodes[state].outputs = append(m.nodes[state].outputs, index)
	}
	var queue []int
	for _, state := range m.nodes[0].next {
		queue = append(queue, state)
	}
	for head := 0; head < len(queue); head++ {
		state := queue[head]
		for char, next := range m.nodes[state].next {
			fallback := m.nodes[state].failure
			for fallback != 0 && m.nodes[fallback].next[char] == 0 {
				fallback = m.nodes[fallback].failure
			}
			m.nodes[next].failure = m.nodes[fallback].next[char]
			terminal := m.nodes[next].failure
			if len(m.nodes[terminal].outputs) == 0 {
				terminal = m.nodes[terminal].outputLink
			}
			m.nodes[next].outputLink = terminal
			queue = append(queue, next)
		}
	}
	return m
}

func (m *matcher) matches(scope, key string) bool {
	state := 0
	for position, char := range key {
		for state != 0 && m.nodes[state].next[char] == 0 {
			state = m.nodes[state].failure
		}
		state = m.nodes[state].next[char]
		end := position + utf8.RuneLen(char)
		for terminal := state; terminal != 0; terminal = m.nodes[terminal].outputLink {
			for _, index := range m.nodes[terminal].outputs {
				rule := m.rules[index]
				if rule.matchesAt(scope, key, end-len(rule.text), end) {
					return true
				}
			}
		}
	}
	return false
}
