package events

import (
	"bd2server/internal/server/wire"
	"fmt"
)

func (e *Economy) AttachPrestigeSkins(design map[uint64]uint64) { e.prestige = design }
func (e *Economy) PrestigeSkinInfo(path string, req []byte) (int, []byte, bool, error) {
	if path != "/PrestigeSkinInfo" {
		return 0, nil, false, nil
	}
	seq, _, err := wire.Varint(req, 1)
	if err != nil || seq == 0 {
		return 425, nil, true, fmt.Errorf("events: missing sequence")
	}
	var out []byte
	seen := map[uint64]bool{}
	for _, item := range e.items.All() {
		if item.Type != 45 || seen[item.ID] {
			continue
		}
		costume, ok := e.prestige[item.ID]
		if !ok {
			return 425, nil, true, fmt.Errorf("events: owned skin missing design")
		}
		seen[item.ID] = true
		b := wire.AppendVarint(nil, 1, costume)
		b = wire.AppendVarint(b, 2, item.ID)
		b = wire.AppendVarint(b, 4, item.TimeValue)
		out = wire.AppendBytes(out, 1, b)
	}
	return 425, out, true, nil
}
