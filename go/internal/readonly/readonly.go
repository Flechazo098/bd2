// Package readonly serves versioned, immutable new-account protocol data.
//
// The seed is a JSON representation of protobuf fields, rather than an HTTP
// response or an encoded protobuf blob.  This makes the runtime independent
// of development captures while preserving unknown nested message layouts.
package readonly

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"bd2server/internal/wire"
)

const Version23413 = "2.34.13"

var ErrInvalidSeed = errors.New("readonly: invalid seed")

// Field is one protobuf field. Bytes is used only when a length-delimited
// value is not itself a protobuf message; Fields retains nested structure.
// Fixed values are little-endian integers, exactly as protobuf specifies.
type Field struct {
	Number  int     `json:"number"`
	Type    int     `json:"type"`
	Varint  uint64  `json:"varint,omitempty"`
	Fixed32 uint32  `json:"fixed32_le,omitempty"`
	Fixed64 uint64  `json:"fixed64_le,omitempty"`
	Bytes   []uint8 `json:"bytes,omitempty"`
	Fields  []Field `json:"fields,omitempty"`
}

type Response struct {
	PacketCode int     `json:"packet_code"`
	Fields     []Field `json:"fields"`
}

type Seed struct {
	Version   string              `json:"version"`
	Responses map[string]Response `json:"responses"`
}

func Load(path string) (*Seed, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("readonly: read seed: %w", err)
	}
	var s Seed
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("readonly: decode seed JSON: %w", err)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *Seed) Write(path string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func (s *Seed) Validate() error {
	if s == nil || s.Version != Version23413 || len(s.Responses) == 0 {
		return ErrInvalidSeed
	}
	for path, r := range s.Responses {
		if len(path) < 2 || path[0] != '/' || r.PacketCode < 0 {
			return fmt.Errorf("%w: response %q", ErrInvalidSeed, path)
		}
		if err := validateFields(r.Fields); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidSeed, path, err)
		}
	}
	return nil
}

func validateFields(fields []Field) error {
	for _, f := range fields {
		if f.Number <= 0 || f.Type < 0 || f.Type > 5 || (f.Type != 0 && f.Type != 1 && f.Type != 2 && f.Type != 5) {
			return fmt.Errorf("bad wire field %d/%d", f.Number, f.Type)
		}
		if f.Type != 2 && (len(f.Bytes) != 0 || len(f.Fields) != 0) {
			return fmt.Errorf("non-length field %d has payload", f.Number)
		}
		if f.Type == 2 && len(f.Bytes) != 0 && len(f.Fields) != 0 {
			return fmt.Errorf("field %d has both bytes and fields", f.Number)
		}
		if err := validateFields(f.Fields); err != nil {
			return err
		}
	}
	return nil
}

// Handle re-encodes a response every time. It intentionally accepts any
// structurally valid sequenced request, matching other initialization routes.
func (s *Seed) Handle(path string, request []byte) (int, []byte, bool, error) {
	r, ok := s.Responses[path]
	if !ok {
		return 0, nil, false, nil
	}
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 {
		return 0, nil, true, fmt.Errorf("readonly: %s invalid sequence", path)
	}
	b, err := encodeFields(r.Fields)
	if err != nil {
		return 0, nil, true, err
	}
	return r.PacketCode, b, true, nil
}

func encodeFields(fields []Field) ([]byte, error) {
	var out []byte
	for _, f := range fields {
		out = binary.AppendUvarint(out, uint64(f.Number<<3|f.Type))
		switch f.Type {
		case 0:
			out = binary.AppendUvarint(out, f.Varint)
		case 1:
			var b [8]byte
			binary.LittleEndian.PutUint64(b[:], f.Fixed64)
			out = append(out, b[:]...)
		case 5:
			var b [4]byte
			binary.LittleEndian.PutUint32(b[:], f.Fixed32)
			out = append(out, b[:]...)
		case 2:
			payload := f.Bytes
			if len(f.Fields) != 0 {
				var err error
				payload, err = encodeFields(f.Fields)
				if err != nil {
					return nil, err
				}
			}
			out = binary.AppendUvarint(out, uint64(len(payload)))
			out = append(out, payload...)
		}
	}
	return out, nil
}

// Service adapts a loaded seed to the session handler interface.
type Service struct{ Seed *Seed }

func (s Service) Handle(path string, request []byte) (int, []byte, bool, error) {
	if s.Seed == nil {
		return 0, nil, false, nil
	}
	return s.Seed.Handle(path, request)
}
