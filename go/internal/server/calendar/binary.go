package calendar

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"unicode/utf8"
)

const MaxFileSize = 8 * 1024 * 1024
const maxRows = 100000
const maxString = 64 * 1024
const headerSize = 46

var magic = []byte{'B', 'D', '2', 'S', 'C', 'H', 0, 0}

type encoder struct {
	data []byte
	err  error
	rows uint64
}

func (e *encoder) u64(v uint64) {
	if e.room(8) {
		e.data = binary.LittleEndian.AppendUint64(e.data, v)
	}
}
func (e *encoder) u32(v uint32) {
	if e.room(4) {
		e.data = binary.LittleEndian.AppendUint32(e.data, v)
	}
}
func (e *encoder) b(v bool) {
	if !e.room(1) {
		return
	}
	if v {
		e.data = append(e.data, 1)
	} else {
		e.data = append(e.data, 0)
	}
}
func (e *encoder) str(v string) {
	if e.err != nil {
		return
	}
	if len(v) > maxString || !utf8.ValidString(v) {
		e.err = fmt.Errorf("calendar: invalid/oversized UTF8 string")
		return
	}
	if !e.room(4 + len(v)) {
		return
	}
	e.u32(uint32(len(v)))
	e.data = append(e.data, v...)
}
func (e *encoder) count(n int) {
	if e.err != nil {
		return
	}
	e.rows += uint64(n)
	if n > maxRows || e.rows > maxRows {
		e.err = fmt.Errorf("calendar: row limit exceeded")
		return
	}
	e.u32(uint32(n))
}
func (e *encoder) room(n int) bool {
	if e.err != nil {
		return false
	}
	if n > MaxFileSize-headerSize-len(e.data) {
		e.err = fmt.Errorf("calendar: file size limit exceeded")
		return false
	}
	return true
}

type decoder struct {
	data []byte
	pos  int
	err  error
	rows uint64
}

func (d *decoder) take(n int) []byte {
	if d.err != nil {
		return nil
	}
	if n < 0 || n > len(d.data)-d.pos {
		d.err = fmt.Errorf("calendar: truncated payload")
		return nil
	}
	v := d.data[d.pos : d.pos+n]
	d.pos += n
	return v
}
func (d *decoder) u64() uint64 {
	v := d.take(8)
	if len(v) != 8 {
		return 0
	}
	return binary.LittleEndian.Uint64(v)
}
func (d *decoder) u32() uint32 {
	v := d.take(4)
	if len(v) != 4 {
		return 0
	}
	return binary.LittleEndian.Uint32(v)
}
func (d *decoder) b() bool {
	v := d.take(1)
	if len(v) != 1 {
		return false
	}
	if v[0] > 1 {
		d.err = fmt.Errorf("calendar: invalid boolean/presence")
	}
	return v[0] == 1
}
func (d *decoder) str() string {
	n := d.u32()
	if n > maxString {
		d.err = fmt.Errorf("calendar: string limit exceeded")
		return ""
	}
	v := d.take(int(n))
	if !utf8.Valid(v) {
		d.err = fmt.Errorf("calendar: invalid UTF8")
	}
	return string(v)
}
func (d *decoder) count() int {
	n := d.u32()
	d.rows += uint64(n)
	if n > maxRows || d.rows > maxRows || uint64(n) > uint64(len(d.data)-d.pos) {
		d.err = fmt.Errorf("calendar: row count limit/truncation")
		return 0
	}
	return int(n)
}

// MarshalBinary emits format 1's fixed-order record payload and checksum.
func MarshalBinary(m Manifest) ([]byte, error) {
	if m.SchemaVersion != 1 {
		return nil, fmt.Errorf("calendar: unsupported schema")
	}
	e := &encoder{}
	e.manifest(m)
	if e.err != nil {
		return nil, e.err
	}
	if len(e.data) > MaxFileSize-headerSize {
		return nil, fmt.Errorf("calendar: file size limit exceeded")
	}
	out := append([]byte(nil), magic...)
	out = binary.LittleEndian.AppendUint16(out, 1)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(e.data)))
	sum := sha256.Sum256(e.data)
	out = append(out, sum[:]...)
	return append(out, e.data...), nil
}

// UnmarshalBinary rejects damaged, unsupported, oversized and trailing data.
func UnmarshalBinary(raw []byte) (Manifest, error) {
	var m Manifest
	if len(raw) < headerSize || len(raw) > MaxFileSize {
		return m, fmt.Errorf("calendar: invalid file size")
	}
	if !bytes.Equal(raw[:8], magic) || binary.LittleEndian.Uint16(raw[8:10]) != 1 {
		return m, fmt.Errorf("calendar: unsupported magic/format")
	}
	n := binary.LittleEndian.Uint32(raw[10:14])
	if uint64(n) != uint64(len(raw)-headerSize) {
		return m, fmt.Errorf("calendar: payload length mismatch")
	}
	payload := raw[headerSize:]
	sum := sha256.Sum256(payload)
	if !bytes.Equal(raw[14:46], sum[:]) {
		return m, fmt.Errorf("calendar: checksum mismatch")
	}
	d := &decoder{data: payload}
	m = d.manifest()
	if d.err != nil {
		return Manifest{}, d.err
	}
	if d.pos != len(payload) {
		return Manifest{}, fmt.Errorf("calendar: trailing payload")
	}
	m.SchemaVersion = 1
	return m, nil
}
