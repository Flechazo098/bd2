// Package wire handles the few protobuf fields needed for bootstrap messages.
// Business messages remain opaque and belong to their own domain handlers.
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

var ErrMalformed = errors.New("malformed protobuf wire data")

type Field struct {
	Number int
	Type   int
	Value  []byte
	Start  int
	End    int
}

func AppendVarint(dst []byte, field int, value uint64) []byte {
	dst = binary.AppendUvarint(dst, uint64(field<<3))
	return binary.AppendUvarint(dst, value)
}

func AppendBytes(dst []byte, field int, value []byte) []byte {
	dst = binary.AppendUvarint(dst, uint64(field<<3|2))
	dst = binary.AppendUvarint(dst, uint64(len(value)))
	return append(dst, value...)
}

func AppendString(dst []byte, field int, value string) []byte {
	return AppendBytes(dst, field, []byte(value))
}

// AppendFixed64 appends a protobuf fixed64 field. It is kept here instead of
// hand-building tags in domain packages so doubles use one canonical encoder.
func AppendFixed64(dst []byte, field int, value uint64) []byte {
	dst = binary.AppendUvarint(dst, uint64(field<<3|1))
	return binary.LittleEndian.AppendUint64(dst, value)
}

func AppendDouble(dst []byte, field int, value float64) []byte {
	return AppendFixed64(dst, field, math.Float64bits(value))
}

// Walk visits wire fields without interpreting the payload or changing its bytes.
func Walk(data []byte, visit func(Field) error) error {
	for pos := 0; pos < len(data); {
		start := pos
		tag, n := binary.Uvarint(data[pos:])
		if n <= 0 || tag>>3 == 0 || tag>>3 > uint64(^uint(0)>>1) {
			return fmt.Errorf("%w: field tag at %d", ErrMalformed, pos)
		}
		pos += n
		field := Field{Number: int(tag >> 3), Type: int(tag & 7), Start: start}
		switch field.Type {
		case 0:
			_, n = binary.Uvarint(data[pos:])
			if n <= 0 {
				return fmt.Errorf("%w: varint at %d", ErrMalformed, pos)
			}
			field.Value = data[pos : pos+n]
			pos += n
		case 1:
			if len(data)-pos < 8 {
				return ErrMalformed
			}
			field.Value = data[pos : pos+8]
			pos += 8
		case 2:
			length, count := binary.Uvarint(data[pos:])
			if count <= 0 || length > uint64(len(data)-pos-count) {
				return fmt.Errorf("%w: length at %d", ErrMalformed, pos)
			}
			pos += count
			field.Value = data[pos : pos+int(length)]
			pos += int(length)
		case 5:
			if len(data)-pos < 4 {
				return ErrMalformed
			}
			field.Value = data[pos : pos+4]
			pos += 4
		default:
			return fmt.Errorf("%w: wire type %d", ErrMalformed, field.Type)
		}
		field.End = pos
		if err := visit(field); err != nil {
			return err
		}
	}
	return nil
}

func Varint(data []byte, number int) (uint64, bool, error) {
	var result uint64
	var found bool
	err := Walk(data, func(field Field) error {
		if field.Number == number && field.Type == 0 {
			result, _ = binary.Uvarint(field.Value)
			found = true
		}
		return nil
	})
	return result, found, err
}

func Bytes(data []byte, number int) ([]byte, bool, error) {
	var result []byte
	var found bool
	err := Walk(data, func(field Field) error {
		if field.Number == number && field.Type == 2 {
			result = field.Value
			found = true
		}
		return nil
	})
	return result, found, err
}

// ReplaceBytes keeps every unrelated field intact, including unknown fields.
func ReplaceBytes(data []byte, number int, value []byte) ([]byte, bool, error) {
	var result []byte
	var replaced bool
	err := Walk(data, func(field Field) error {
		if field.Number == number && field.Type == 2 && !replaced {
			result = AppendBytes(result, number, value)
			replaced = true
		} else {
			result = append(result, data[field.Start:field.End]...)
		}
		return nil
	})
	return result, replaced, err
}

// ReplaceVarint replaces the first varint field with number while preserving
// every unrelated (including unknown) protobuf field byte-for-byte. If the
// field is absent it is appended, which is required for proto3 zero values
// omitted from a versioned template.
func ReplaceVarint(data []byte, number int, value uint64) ([]byte, bool, error) {
	var result []byte
	var replaced bool
	err := Walk(data, func(field Field) error {
		if field.Number == number && field.Type == 0 && !replaced {
			result = AppendVarint(result, number, value)
			replaced = true
		} else {
			result = append(result, data[field.Start:field.End]...)
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if !replaced {
		result = AppendVarint(result, number, value)
	}
	return result, replaced, nil
}
