package realtime

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"

	"bd2server/internal/server/runtime/room"
)

const (
	AuthMagic   = "BD2RT001"
	AckMagic    = "BD2RA001"
	UDPMagic    = "BD2UDP01"
	UDPOverhead = 76
)

type AuthRequest struct {
	Ticket     string `json:"ticket"`
	Channel    string `json:"channel"`
	InstanceID string `json:"instance_id"`
}
type AuthAck struct {
	ConnectionID string `json:"connection_id"`
	Epoch        uint64 `json:"epoch"`
	UDPKey       string `json:"udp_key,omitempty"`
}

func ReadFrame(r io.Reader, maxBytes int) (room.Frame, error) {
	var header [20]byte
	if _, e := io.ReadFull(r, header[:]); e != nil {
		return room.Frame{}, e
	}
	typ := int32(binary.LittleEndian.Uint32(header[:4]))
	size := int32(binary.LittleEndian.Uint32(header[4:8]))
	if typ <= 0 || typ > 224 || size < 0 || int64(size) > int64(maxBytes) {
		return room.Frame{}, errors.New("realtime: invalid native frame")
	}
	f := room.Frame{Type: typ, Sequence: int32(binary.LittleEndian.Uint32(header[8:12])), OwnerIndex: int64(binary.LittleEndian.Uint64(header[12:20])), Payload: make([]byte, int(size))}
	_, e := io.ReadFull(r, f.Payload)
	return f, e
}
func EncodeFrame(f room.Frame) []byte {
	b := make([]byte, 20, 20+len(f.Payload))
	binary.LittleEndian.PutUint32(b[:4], uint32(f.Type))
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(f.Payload)))
	binary.LittleEndian.PutUint32(b[8:12], uint32(f.Sequence))
	binary.LittleEndian.PutUint64(b[12:20], uint64(f.OwnerIndex))
	return append(b, f.Payload...)
}

type replayWindow struct {
	highest uint64
	bits    uint64
}

func (w *replayWindow) accept(sequence uint64) bool {
	if sequence == 0 {
		return false
	}
	if sequence > w.highest {
		delta := sequence - w.highest
		if delta >= 64 {
			w.bits = 0
		} else {
			w.bits <<= delta
		}
		w.bits |= 1
		w.highest = sequence
		return true
	}
	delta := w.highest - sequence
	if delta >= 64 || w.bits&(uint64(1)<<delta) != 0 {
		return false
	}
	w.bits |= uint64(1) << delta
	return true
}
func directionalKey(key []byte, direction string) []byte {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte("bd2-realtime-udp:" + direction))
	return m.Sum(nil)
}
func encodeDatagram(id []byte, epoch, sequence uint64, payload, key []byte) []byte {
	b := make([]byte, 44, 44+len(payload)+sha256.Size)
	copy(b, UDPMagic)
	copy(b[8:24], id)
	binary.LittleEndian.PutUint64(b[24:32], epoch)
	binary.LittleEndian.PutUint64(b[32:40], sequence)
	binary.LittleEndian.PutUint32(b[40:44], uint32(len(payload)))
	b = append(b, payload...)
	m := hmac.New(sha256.New, key)
	_, _ = m.Write(b)
	return append(b, m.Sum(nil)...)
}
func verifyDatagram(b, key []byte) bool {
	if len(b) < UDPOverhead || string(b[:8]) != UDPMagic || int64(binary.LittleEndian.Uint32(b[40:44])) != int64(len(b)-UDPOverhead) {
		return false
	}
	m := hmac.New(sha256.New, key)
	_, _ = m.Write(b[:len(b)-sha256.Size])
	return hmac.Equal(m.Sum(nil), b[len(b)-sha256.Size:])
}
