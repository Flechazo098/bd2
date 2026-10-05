package world

import (
	"testing"

	"bd2server/internal/server/wire"
)

type fakeHuntingGround struct {
	calls []int
	row   []byte
}

func (h *fakeHuntingGround) EnsureForPack(pack int) ([]byte, error) {
	h.calls = append(h.calls, pack)
	return h.row, nil
}

func TestPackLoadRestoresActualHuntingState(t *testing.T) {
	s := testService()
	h := &fakeHuntingGround{row: wire.AppendVarint(nil, 2, 91)}
	if err := s.AttachHuntingGround(h); err != nil {
		t.Fatal(err)
	}
	out, err := s.packInfoFor(s.seed.PackID)
	if err != nil {
		t.Fatal(err)
	}
	b, ok, err := wire.Bytes(out, 12)
	if err != nil || !ok {
		t.Fatalf("ground missing: %v", err)
	}
	if id, _, _ := wire.Varint(b, 2); id != 91 {
		t.Fatalf("seed placeholder replaced state: %d", id)
	}
	if len(h.calls) != 1 || h.calls[0] != s.seed.PackID {
		t.Fatal("wrong loaded pack")
	}
}
