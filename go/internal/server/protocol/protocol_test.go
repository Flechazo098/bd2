package protocol

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"bd2server/internal/server/cryptox"
)

func TestEncode(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	proto := []byte{8, 42}
	raw, err := Encode(19, proto, key, 123)
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	decoded, err := cryptox.DecryptBase64Payload(envelope.Data, key)
	if err != nil || string(decoded) != string(proto) || envelope.PacketCode != 19 || envelope.Length != 4 || envelope.ServerNowTime != 123 {
		t.Fatalf("unexpected encoded response: %+v data=%v err=%v", envelope, decoded, err)
	}
}

func TestNotifyIsPlainBase64SeparateFromEncryptedResponse(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	notify := []byte{0x12, 6, 8, 42, 16, 7, 24, 1}
	raw, err := EncodeWithNotify(167, nil, key, 123, notify)
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(envelope.Notify)
	if err != nil || string(decoded) != string(notify) {
		t.Fatalf("notify=%x err=%v", decoded, err)
	}
	response, err := cryptox.DecryptBase64Payload(envelope.Data, key)
	if err != nil || len(response) != 0 {
		t.Fatalf("response=%x err=%v", response, err)
	}
}

func TestBatchPreservesOrderAndRejectsDuplicates(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	input := []BatchRequest{{Path: "/B", RequestData: base64.StdEncoding.EncodeToString([]byte{8, 1})}, {Path: "/A", RequestData: base64.StdEncoding.EncodeToString([]byte{8, 2})}}
	plain, _ := json.Marshal(input)
	cipher, err := cryptox.EncryptBase64(plain, key)
	if err != nil {
		t.Fatal(err)
	}
	got, proto, err := DecodeBatchRequest([]byte(cipher), key)
	if err != nil || got[0].Path != "/B" || got[1].Path != "/A" || proto[1][1] != 2 {
		t.Fatalf("batch decode: %v %v %v", got, proto, err)
	}
	input[1].Path = "/B"
	plain, _ = json.Marshal(input)
	cipher, _ = cryptox.EncryptBase64(plain, key)
	if _, _, err := DecodeBatchRequest([]byte(cipher), key); err == nil {
		t.Fatal("accepted duplicate path")
	}
}
