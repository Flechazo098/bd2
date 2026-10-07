package calendar

import (
	"fmt"
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

// MarshalBinary emits format 1's fixed-order record payload and checksum.

// UnmarshalBinary rejects damaged, unsupported, oversized and trailing data.
