package eventplay

import (
	"bd2server/internal/server/wire"
	"fmt"
	"sort"
	"strings"
)

func specializedInfo(st *snapshot, uid uint64, f string) []byte {
	var out []byte
	if f == "Action" || f == "Survival" || f == "Defense" {
		out = wire.AppendVarint(out, 1, uid)
	}
	for _, r := range st.Records {
		if r.UID != uid || r.Family != f {
			continue
		}
		switch f {
		case "Sichuan":
			v := wire.AppendVarint(nil, 1, uid)
			v = wire.AppendVarint(v, 6, r.Best)
			out = wire.AppendBytes(out, 1, v)
		case "Rhythm":
			v := wire.AppendVarint(nil, 1, r.Stage)
			v = wire.AppendVarint(v, 2, r.Mode)
			v = wire.AppendVarint(v, 3, r.Best)
			out = wire.AppendBytes(out, 1, v)
		case "Action":
			v := wire.AppendVarint(nil, 1, r.Stage)
			v = wire.AppendVarint(v, 2, r.Best)
			out = wire.AppendBytes(out, 2, v)
		case "Hopscotch":
			v := wire.AppendVarint(nil, 1, r.Stage)
			if r.Best > 0 {
				v = wire.AppendVarint(v, 2, 1)
			}
			out = wire.AppendBytes(out, 2, v)
		case "Survival":
			out = wire.AppendVarint(out, 4, r.Best)
			for stage, clear := range r.Clears {
				if !clear {
					continue
				}
				var group, id uint64
				if _, e := fmt.Sscanf(stage, "map:%d:%d", &group, &id); e == nil {
					v := wire.AppendVarint(nil, 1, group)
					v = wire.AppendVarint(v, 2, id)
					out = wire.AppendBytes(out, 8, v)
				}
			}
		}
	}
	if f == "Survival" {
		prefix := fmt.Sprintf("%d:", uid)
		for key, level := range st.Upgrades {
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			var id uint64
			_, _ = fmt.Sscanf(key[len(prefix):], "%d", &id)
			v := wire.AppendVarint(nil, 1, id)
			v = wire.AppendVarint(v, 2, level)
			out = wire.AppendBytes(out, 10, v)
		}
	}
	return out
}
func rankingWire(st *snapshot, uid uint64, f string, record bool) []byte {
	keys := []string{}
	for k, r := range st.Records {
		if r.UID == uid && r.Family == f && r.Best > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []byte
	for _, k := range keys {
		r := st.Records[k]
		var b []byte
		switch f {
		case "Survival":
			b = wire.AppendVarint(nil, 1, 1)
			b = wire.AppendVarint(b, 4, r.Best)
			if record {
				b = wire.AppendVarint(nil, 1, r.Best)
				b = wire.AppendDouble(b, 2, 100)
			}
		case "Hopscotch":
			b = wire.AppendVarint(nil, 1, 1)
			b = wire.AppendVarint(b, 4, r.Best)
			if record {
				b = wire.AppendVarint(nil, 1, r.Stage)
				b = wire.AppendVarint(b, 2, r.Best)
				b = wire.AppendDouble(b, 4, 100)
			}
		case "Action":
			b = wire.AppendVarint(nil, 3, 1)
			b = wire.AppendVarint(b, 4, r.Best)
			if record {
				b = wire.AppendVarint(nil, 1, r.Stage)
				b = wire.AppendVarint(b, 2, r.Best)
				b = wire.AppendDouble(b, 3, 100)
			}
		case "Sichuan":
			b = wire.AppendVarint(nil, 7, 1)
			b = wire.AppendVarint(b, 8, r.Best)
			if record {
				b = wire.AppendVarint(nil, 1, r.Best)
				b = wire.AppendDouble(b, 2, 100)
			}
		case "Rhythm":
			b = wire.AppendVarint(nil, 1, 1)
			b = wire.AppendVarint(b, 4, r.Best)
			if record {
				b = wire.AppendVarint(nil, 1, r.Best)
				b = wire.AppendVarint(b, 2, r.Stage)
				b = wire.AppendDouble(b, 3, 100)
			}
		default:
			b = wire.AppendVarint(nil, 1, 1)
			b = wire.AppendVarint(b, 4, r.Best)
		}
		out = wire.AppendBytes(out, 1, b)
	}
	return out
}
func (s *Service) claimScore(path string, uid uint64, f string, next *snapshot, key string) ([]byte, error) {
	for k, r := range next.Records {
		if r.UID != uid || r.Family != f || r.Best == 0 {
			continue
		}
		point, rewards, e := s.scoreRewards(r.Game, r.Best)
		if e != nil {
			return nil, e
		}
		day := uint64(s.now().UnixMilli()) / 86400000
		if r.Date != day {
			r.Paid = 0
		}
		if point <= r.Paid {
			return nil, fmt.Errorf("eventplay: daily best reward already paid")
		}
		_, old, e := s.scoreRewards(r.Game, r.Paid)
		if e != nil {
			return nil, e
		}
		bundle, e := s.grant("eventplay:"+key, rewardDifference(old, rewards))
		if e != nil {
			return nil, e
		}
		r.Paid = point
		r.Date = day
		next.Records[k] = r
		if strings.HasSuffix(path, "QuickReward") {
			return wire.AppendBytes(nil, 1, bundle), nil
		}
		out := wire.AppendVarint(nil, 1, point)
		return wire.AppendBytes(out, 2, bundle), nil
	}
	return nil, fmt.Errorf("eventplay: no completed score to reward")
}
