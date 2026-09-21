package session

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"bd2server/internal/cryptox"
	"bd2server/internal/protocol"
	"bd2server/internal/transport"
	"bd2server/internal/wire"
)

type fakeLogin struct{}

func (fakeLogin) Login(request, key []byte) ([]byte, error) {
	if _, ok, err := wire.Varint(request, 1); err != nil || !ok {
		return nil, errors.New("missing seq")
	}
	user := wire.AppendBytes(nil, 3, key)
	return wire.AppendBytes(nil, 1, user), nil
}

type fakeDomain struct{}

func (fakeDomain) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path == "/EmptyInfo" {
		return 77, nil, true, nil
	}
	return 0, nil, false, nil
}

func login(t *testing.T, server *Server) transport.RawReply {
	t.Helper()
	request := wire.AppendVarint(nil, 1, 1)
	body, err := cryptox.EncryptBase64Payload(request, cryptox.Key())
	if err != nil {
		t.Fatal(err)
	}
	reply, err := server.DispatchRaw("/LoginUser", []byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var envelope protocol.Envelope
	if err := json.Unmarshal(reply.Body, &envelope); err != nil {
		t.Fatal(err)
	}
	proto, err := cryptox.DecryptBase64Payload(envelope.Data, cryptox.Key())
	if err != nil || envelope.PacketCode != 3 || len(proto) == 0 || reply.Cookie == "" {
		t.Fatalf("login response: %+v proto=%d err=%v", envelope, len(proto), err)
	}
	return reply
}

func TestNativeLoginAndBatch(t *testing.T) {
	server, err := NewServer(fakeLogin{}, fakeDomain{})
	if err != nil {
		t.Fatal(err)
	}
	reply := login(t, server)
	requests := []protocol.BatchRequest{{Path: "/EmptyInfo", RequestData: base64.StdEncoding.EncodeToString(wire.AppendVarint(nil, 1, 2))}}
	plain, _ := json.Marshal(requests)
	body, _ := cryptox.EncryptBase64(plain, server.KeyForTest())
	batch, err := server.DispatchRaw("/BatchRequest", []byte(body), "s="+reply.Cookie)
	if err != nil {
		t.Fatal(err)
	}
	var items []protocol.BatchResponse
	if err := json.Unmarshal(batch.Body, &items); err != nil || len(items) != 1 || items[0].Path != "/EmptyInfo" || items[0].ResponseData.PacketCode != 77 {
		t.Fatalf("batch response: %+v err=%v", items, err)
	}
}

func TestSessionRejectsMissingCookieAndUnknownPath(t *testing.T) {
	server, _ := NewServer(fakeLogin{}, fakeDomain{})
	if _, err := server.DispatchRaw("/EmptyInfo", nil, ""); err == nil {
		t.Fatal("authenticated endpoint accepted missing cookie")
	}
	reply := login(t, server)
	request := wire.AppendVarint(nil, 1, 99)
	body, _ := cryptox.EncryptBase64Payload(request, server.KeyForTest())
	if _, err := server.DispatchRaw("/InventedPacket", []byte(body), "s="+reply.Cookie); !errors.Is(err, transport.ErrNotImplemented) {
		t.Fatalf("unknown endpoint did not fail closed: %v", err)
	}
}

func TestNativeProgress(t *testing.T) {
	server, _ := NewServer(fakeLogin{})
	reply := login(t, server)
	cookie := "s=" + reply.Cookie
	position := wire.AppendVarint(nil, 1, 999001)
	position = wire.AppendVarint(position, 2, 21)
	position = wire.AppendString(position, 3, `{"MapId":211,"PlayerPosition":{"x":1,"y":2,"z":3},"ColleaguePositions":null}`)
	body, _ := cryptox.EncryptBase64Payload(position, server.KeyForTest())
	if _, err := server.DispatchRaw("/SaveUserPosition", []byte(body), cookie); err != nil {
		t.Fatal(err)
	}
	saved, found := server.ProgressForTest().Position()
	if !found || saved.PackID != 21 || saved.Position.MapID != 211 {
		t.Fatalf("position not stored: %+v", saved)
	}
	quest := wire.AppendVarint(nil, 1, 999002)
	quest = wire.AppendVarint(quest, 2, 12)
	quest = wire.AppendVarint(quest, 3, 21)
	quest = wire.AppendBytes(quest, 4, []byte{121})
	body, _ = cryptox.EncryptBase64Payload(quest, server.KeyForTest())
	if _, err := server.DispatchRaw("/QuestUpdate", []byte(body), cookie); err != nil {
		t.Fatal(err)
	}
	if stored, ok := server.ProgressForTest().Quest(12); !ok || stored.PackID != 21 {
		t.Fatalf("quest not stored: %+v/%v", stored, ok)
	}
}
