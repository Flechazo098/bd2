package eventplay

import (
	"bd2server/internal/server/wire"
	"sort"
)

func summaryWire(st *snapshot, record bool) []byte {
	keys := []string{}
	for k, v := range st.Records {
		if v.Best > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []byte
	for _, k := range keys {
		v := st.Records[k]
		field := map[string]int{"Survival": 1, "Sichuan": 2, "Action": 3, "Rhythm": 5, "Hopscotch": 6}[v.Family]
		if field == 0 {
			continue
		}
		var b []byte
		switch v.Family {
		case "Survival":
			b = wire.AppendVarint(nil, 2, v.Best)
		case "Sichuan":
			b = wire.AppendDouble(nil, 2, float64(v.Best))
		case "Action":
			b = wire.AppendVarint(nil, 2, v.Best)
			b = wire.AppendVarint(b, 3, v.Stage)
		case "Rhythm":
			b = wire.AppendVarint(nil, 2, v.Best)
			b = wire.AppendVarint(b, 3, v.Stage)
		case "Hopscotch":
			b = wire.AppendVarint(nil, 1, v.Stage)
			b = wire.AppendVarint(b, 3, v.Best)
		}
		if record {
			switch v.Family {
			case "Survival":
				b = wire.AppendVarint(nil, 1, v.Best)
				b = wire.AppendDouble(b, 2, 100)
			case "Sichuan":
				b = wire.AppendDouble(nil, 1, float64(v.Best))
				b = wire.AppendDouble(b, 2, 100)
			case "Action":
				b = wire.AppendVarint(nil, 1, v.Stage)
				b = wire.AppendVarint(b, 2, v.Best)
				b = wire.AppendDouble(b, 3, 100)
			case "Rhythm":
				b = wire.AppendVarint(nil, 1, v.Best)
				b = wire.AppendVarint(b, 2, v.Stage)
				b = wire.AppendDouble(b, 3, 100)
			case "Hopscotch":
				b = wire.AppendVarint(nil, 1, v.Stage)
				b = wire.AppendVarint(b, 2, v.Best)
				b = wire.AppendDouble(b, 4, 100)
			}
		}
		out = wire.AppendBytes(out, field, b)
	}
	return out
}
