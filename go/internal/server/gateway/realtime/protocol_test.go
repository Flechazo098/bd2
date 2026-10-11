package realtime

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"testing"

	"bd2server/internal/server/runtime/room"
)

func TestDatagramAuthenticatesNativeFrameAndDirection(t *testing.T) {
	master := bytes.Repeat([]byte{0x61}, 32)
	id := bytes.Repeat([]byte{0x42}, 16)
	f := room.Frame{Type: 84, Sequence: 9, OwnerIndex: 77, Payload: []byte{0x12, 0x01, 'g'}}
	packet := encodeDatagram(id, 123, 7, EncodeFrame(f), directionalKey(master, "client"))
	if string(packet[:8]) != "BD2UDP01" || binary.LittleEndian.Uint64(packet[24:32]) != 123 || binary.LittleEndian.Uint64(packet[32:40]) != 7 {
		t.Fatal("connection identity was not bound to datagram")
	}
	derive := hmac.New(sha256.New, master)
	derive.Write([]byte("bd2-realtime-udp:client"))
	mac := hmac.New(sha256.New, derive.Sum(nil))
	mac.Write(packet[:len(packet)-32])
	if !hmac.Equal(packet[len(packet)-32:], mac.Sum(nil)) {
		t.Fatal("datagram MAC does not cover header and native frame")
	}
	if verifyDatagram(packet, directionalKey(master, "server")) {
		t.Fatal("client message accepted in server direction")
	}
	packet[45] ^= 1
	if verifyDatagram(packet, directionalKey(master, "client")) {
		t.Fatal("tampered native frame accepted")
	}
}

func TestReplayWindowAllowsReorderingOnce(t *testing.T) {
	var w replayWindow
	for _, s := range []uint64{10, 8, 9, 100} {
		if !w.accept(s) {
			t.Fatalf("new sequence %d rejected", s)
		}
	}
	for _, s := range []uint64{0, 100, 9, 10, 36} {
		if w.accept(s) {
			t.Fatalf("replayed or expired sequence %d accepted", s)
		}
	}
	if !w.accept(99) {
		t.Fatal("unseen recent sequence rejected")
	}
}
