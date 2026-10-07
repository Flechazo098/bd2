package eventgames

import (
	"bd2server/internal/server/design/gamedata"
)

func rewardCount(d *gamedata.EventGame, count uint64) uint64 {
	max := uint64(0)
	for _, r := range d.Cells {
		if r.Count > max {
			max = r.Count
		}
	}
	if count > max {
		return max
	}
	return count
}
func completeLine(g *GameState, n, typ, index uint64) bool {
	if n == 0 {
		return false
	}
	for i := range n {
		var pos uint64
		switch typ {
		case 1:
			pos = index*n + i
		case 2:
			pos = i*n + index
		case 0:
			if index == 0 {
				pos = i*n + i
			} else {
				pos = i*n + (n - 1 - i)
			}
		default:
			return false
		}
		if pos >= uint64(len(g.Board)) || !contains(g.Opened, pos) {
			return false
		}
	}
	return true
}
