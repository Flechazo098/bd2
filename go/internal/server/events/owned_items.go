package events

import (
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
	"fmt"
)

func (e *Economy) AttachOwnedItemDesign(d map[uint64]map[uint64]bool) { e.ownedDesign = d }

// ID card items are loaded through ItemInfo; there is no IdCardInfo ownership
// endpoint. AvatarInfo separately includes its owned ItemDBInfo list.
func (e *Economy) OwnedItemInfo(path string, req []byte) (int, []byte, bool, error) {
	if path != "/AvatarInfo" {
		return 0, nil, false, nil
	}
	seq, _, err := wire.Varint(req, 1)
	if err != nil || seq == 0 {
		return 467, nil, true, fmt.Errorf("events: missing sequence")
	}
	var out []byte
	for _, item := range e.items.All() {
		if item.Type == 49 || item.Type == 50 || item.Type == 61 {
			out = wire.AppendBytes(out, 2, player.ItemWire(item))
		}
	}
	return 467, out, true, nil
}
