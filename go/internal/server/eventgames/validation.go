package eventgames

import (
	"bd2server/internal/server/gamedata"
	"fmt"
	"strconv"
)

func validateSaved(key string, g GameState, d *gamedata.EventGame) error {
	if key != strconv.FormatUint(g.UID, 10) || g.ID != d.ID || g.Type != d.Type {
		return fmt.Errorf("eventgames: invalid saved identity")
	}
	if g.Type == 12 {
		if g.Position >= uint64(len(d.Cells)) {
			return fmt.Errorf("eventgames: invalid saved position")
		}
		return nil
	}
	if g.Type == 19 {
		if g.Free > d.Free || g.SinceSpecial > g.Tries {
			return fmt.Errorf("eventgames: invalid saved roulette counters")
		}
		return nil
	}
	expected := 0
	for _, r := range d.Cells {
		if r.Count == rewardCount(d, g.Clear) {
			expected++
		}
	}
	if len(g.Board) != expected || expected == 0 {
		return fmt.Errorf("eventgames: saved board size differs from design")
	}
	if g.Type == 13 && (d.Columns == 0 || uint64(expected) != d.Columns*d.Columns) {
		return fmt.Errorf("eventgames: invalid bingo board dimensions")
	}
	seen := map[uint64]bool{}
	for _, id := range g.Board {
		if seen[id] {
			return fmt.Errorf("eventgames: duplicate saved board tile")
		}
		seen[id] = true
		found := false
		for _, r := range d.Cells {
			if r.ID == id && r.Count == rewardCount(d, g.Clear) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("eventgames: saved tile outside design")
		}
	}
	seen = map[uint64]bool{}
	for _, id := range g.Opened {
		if seen[id] || g.Type == 13 && id >= uint64(len(g.Board)) || g.Type == 17 && !contains(g.Board, id) {
			return fmt.Errorf("eventgames: invalid saved open tile")
		}
		seen[id] = true
	}
	lineSeen := map[uint64]bool{}
	for _, key := range g.Lines {
		if lineSeen[key] {
			return fmt.Errorf("eventgames: duplicate saved line")
		}
		lineSeen[key] = true
		found := false
		for _, r := range d.Lines {
			if g.Type == 13 && r.Count == rewardCount(d, g.Clear) && key == r.LineType*1000+r.LineIndex && completeLine(&g, d.Columns, r.LineType, r.LineIndex) {
				found = true
			}
		}
		for _, r := range d.Complete {
			if g.Type == 17 && r.Count == g.Clear && r.ID == key {
				all := true
				for _, id := range r.Members {
					all = all && contains(g.Opened, id)
				}
				found = all
			}
		}
		if !found {
			return fmt.Errorf("eventgames: saved completed line invalid")
		}
	}
	return nil
}
