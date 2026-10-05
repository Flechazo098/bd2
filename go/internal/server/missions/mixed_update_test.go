package missions

import (
	"bd2server/internal/server/wire"
	"testing"
)

func TestSplitMixedMissionUpdates(t *testing.T) {
	regular := wire.AppendVarint(nil, 1, 1)
	regular = wire.AppendVarint(regular, 2, 101)
	regular = wire.AppendVarint(regular, 3, 2)
	event := wire.AppendVarint(nil, 1, 9)
	event = wire.AppendVarint(event, 2, 10)
	event = wire.AppendVarint(event, 3, 1)
	event = wire.AppendVarint(event, 4, 77)
	req := wire.AppendVarint(nil, 1, 40)
	req = wire.AppendBytes(req, 2, regular)
	req = wire.AppendBytes(req, 2, event)
	r, e, err := splitMissionUpdates(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range [][]byte{r, e} {
		seq, _, _ := wire.Varint(part, 1)
		if seq != 40 {
			t.Fatal("lost sequence")
		}
	}
	rr, _, _ := wire.Bytes(r, 2)
	ee, _, _ := wire.Bytes(e, 2)
	rid, _, _ := wire.Varint(rr, 2)
	eid, _, _ := wire.Varint(ee, 4)
	if rid != 101 || eid != 77 {
		t.Fatal("mixed update was not split by event identity")
	}
}
