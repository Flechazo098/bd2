package feature

import (
	"bytes"
	"path/filepath"
	"testing"

	"bd2server/internal/fixture"
	"bd2server/internal/wire"
)

// TestCaptureCompatibility is an optional development-time protocol audit.
// Normal server execution never opens the capture.
func TestCaptureCompatibility(t *testing.T) {
	root := filepath.Join("..", "..", "..", "data", "capture", "2.34.13", "20260920-003254")
	set, err := fixture.Load(root)
	if err != nil {
		t.Skipf("optional capture unavailable: %v", err)
	}
	key, err := set.SessionKey()
	if err != nil {
		t.Fatal(err)
	}
	for batch := 1; batch <= 2; batch++ {
		body, err := set.BatchResponse(batch)
		if err != nil {
			t.Fatal(err)
		}
		items, decoded, err := fixture.DecodeBatch(body, []byte(key))
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if _, _, handled, err := Handle(item.Path, wire.AppendVarint(nil, 1, 1)); err != nil || !handled {
				t.Logf("batch=%d pending domain endpoint=%s", batch, item.Path)
			}
			if _, known := EmptyPacketCodes()[item.Path]; known {
				if len(decoded[item.Path]) != 0 {
					t.Errorf("%s captured protobuf was not empty", item.Path)
				}
			}
			if _, _, known := initialResponse(item.Path); known {
				code, generated, handled, err := Handle(item.Path, wire.AppendVarint(nil, 1, 1))
				if err != nil || !handled || code != item.Response.PacketCode || !bytes.Equal(generated, decoded[item.Path]) {
					t.Errorf("%s native response differs from observed 2.34.13 new account: code=%d vs %d proto=%x vs %x err=%v", item.Path, code, item.Response.PacketCode, generated, decoded[item.Path], err)
				}
			}
		}
	}
}
