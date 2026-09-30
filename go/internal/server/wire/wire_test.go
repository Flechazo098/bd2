package wire

import (
	"bytes"
	"errors"
	"testing"
)

func TestWalkAndReplace(t *testing.T) {
	original := AppendVarint(nil, 1, 42)
	original = AppendString(original, 2, "old")
	original = AppendBytes(original, 15, []byte{0, 1, 2})
	modified, replaced, err := ReplaceBytes(original, 2, []byte("longer"))
	if err != nil || !replaced {
		t.Fatalf("replace: %v, %v", replaced, err)
	}
	v, ok, err := Varint(modified, 1)
	if err != nil || !ok || v != 42 {
		t.Fatalf("preserved varint: %d %v %v", v, ok, err)
	}
	x, ok, err := Bytes(modified, 2)
	if err != nil || !ok || string(x) != "longer" {
		t.Fatalf("replacement: %q %v %v", x, ok, err)
	}
	unknown, _, _ := Bytes(modified, 15)
	if !bytes.Equal(unknown, []byte{0, 1, 2}) {
		t.Fatalf("unknown field changed: %x", unknown)
	}
}

func TestTruncated(t *testing.T) {
	if err := Walk([]byte{0x12, 0x03, 0x01}, func(Field) error { return nil }); !errors.Is(err, ErrMalformed) {
		t.Fatalf("expected malformed: %v", err)
	}
}

func TestReplaceVarintAndAppendMissing(t *testing.T) {
	original := AppendVarint(nil, 1, 7)
	original = AppendString(original, 2, "preserved")
	modified, replaced, err := ReplaceVarint(original, 1, 300)
	if err != nil || !replaced {
		t.Fatalf("replace existing: replaced=%v err=%v", replaced, err)
	}
	value, found, err := Varint(modified, 1)
	if err != nil || !found || value != 300 {
		t.Fatalf("existing value=%d found=%v err=%v", value, found, err)
	}
	modified, replaced, err = ReplaceVarint(modified, 9, 0)
	if err != nil || replaced {
		t.Fatalf("append missing: replaced=%v err=%v", replaced, err)
	}
	value, found, err = Varint(modified, 9)
	if err != nil || !found || value != 0 {
		t.Fatalf("appended zero=%d found=%v err=%v", value, found, err)
	}
}
