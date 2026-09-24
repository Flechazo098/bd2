package session

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"bd2server/internal/accountstate"
	"bd2server/internal/cryptox"
	"bd2server/internal/protocol"
	"bd2server/internal/stateio"
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

type fakeStateGate struct{ err error }

func (g *fakeStateGate) Check() error                { return g.err }
func (g *fakeStateGate) Load(string) ([]byte, error) { return nil, nil }
func (g *fakeStateGate) Save(string, []byte) error   { return nil }
func (g *fakeStateGate) Close() error                { return nil }
func (g *fakeStateGate) BeginOperation() (stateio.RequestOperation, error) {
	if g.err != nil {
		return nil, g.err
	}
	return fakeOperation{}, nil
}

type fakeOperation struct{}

func (fakeOperation) Commit() error   { return nil }
func (fakeOperation) Rollback() error { return nil }

func TestFormationTimingPath(t *testing.T) {
	for _, path := range []string{"/PresetInfo", "/PresetSave", "/DeckSave", "/DeckCostumeSettingSave", "/FieldDeckInfo", "/EquipBatchUse"} {
		if !formationTimingPath(path) {
			t.Fatalf("formation path %s is not timed", path)
		}
	}
	for _, path := range []string{"/LoginUser", "/GachaInfo", "/EquipInfo"} {
		if formationTimingPath(path) {
			t.Fatalf("unrelated path %s is marked as formation timing", path)
		}
	}
}

type mutatingDomain struct {
	store stateio.Store
	fail  bool
}

func (d *mutatingDomain) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path == "/InjectedFailure" {
		return 0, nil, true, errors.New("injected domain failure")
	}
	if path != "/MutateTwoFiles" {
		return 0, nil, false, nil
	}
	if err := d.store.Save("wallet", []byte("new-wallet")); err != nil {
		return 0, nil, true, err
	}
	if err := d.store.Save("items", []byte("new-items")); err != nil {
		return 0, nil, true, err
	}
	if d.fail {
		return 0, nil, true, errors.New("injected domain failure")
	}
	return 77, nil, true, nil
}

func TestBatchUsesOneAccountTransaction(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.db")
	repository, err := accountstate.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	for name, content := range map[string]string{"wallet": "old-wallet", "items": "old-items"} {
		if err := repository.Save(name, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	server, _ := NewServer(fakeLogin{}, &mutatingDomain{store: repository})
	if err := server.AttachStateStore(repository); err != nil {
		t.Fatal(err)
	}
	reply := login(t, server)
	requests := []protocol.BatchRequest{
		{Path: "/MutateTwoFiles", RequestData: base64.StdEncoding.EncodeToString(wire.AppendVarint(nil, 1, 2))},
		{Path: "/InjectedFailure", RequestData: base64.StdEncoding.EncodeToString(wire.AppendVarint(nil, 1, 3))},
	}
	plain, _ := json.Marshal(requests)
	body, _ := cryptox.EncryptBase64(plain, server.KeyForTest())
	if _, err := server.DispatchRaw("/BatchRequest", []byte(body), "s="+reply.Cookie); err == nil {
		t.Fatal("partially failing batch was accepted")
	}
	verified, err := accountstate.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer verified.Close()
	for name, want := range map[string]string{"wallet": "old-wallet", "items": "old-items"} {
		got, err := verified.Load(name)
		if err != nil || string(got) != want {
			t.Fatalf("batch rollback %s=%q err=%v", name, got, err)
		}
	}
}

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

func TestStateGateStopsEveryRequestAfterPersistenceFailure(t *testing.T) {
	server, _ := NewServer(fakeLogin{}, fakeDomain{})
	gate := &fakeStateGate{}
	if err := server.AttachStateStore(gate); err != nil {
		t.Fatal(err)
	}
	reply := login(t, server)
	gate.err = errors.New("uncertain transaction")
	request := wire.AppendVarint(nil, 1, 2)
	body, _ := cryptox.EncryptBase64Payload(request, server.KeyForTest())
	if _, err := server.DispatchRaw("/EmptyInfo", []byte(body), "s="+reply.Cookie); err == nil {
		t.Fatal("request passed a failed account state gate")
	}
	if _, err := server.DispatchRaw("/LoginUser", []byte(body), ""); err == nil {
		t.Fatal("login passed a failed account state gate")
	}
}

func TestAuthenticatedRequestTransactionCommitsOrRollsBackAllFiles(t *testing.T) {
	for _, test := range []struct {
		name string
		fail bool
		want string
	}{
		{"commit", false, "new-"},
		{"rollback", true, "old-"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			statePath := filepath.Join(root, "state.db")
			repository, err := accountstate.Open(statePath)
			if err != nil {
				t.Fatal(err)
			}
			defer repository.Close()
			for name, content := range map[string]string{"wallet": "old-wallet", "items": "old-items"} {
				if err := repository.Save(name, []byte(content)); err != nil {
					t.Fatal(err)
				}
			}
			server, _ := NewServer(fakeLogin{}, &mutatingDomain{store: repository, fail: test.fail})
			if err := server.AttachStateStore(repository); err != nil {
				t.Fatal(err)
			}
			reply := login(t, server)
			request := wire.AppendVarint(nil, 1, 2)
			body, _ := cryptox.EncryptBase64Payload(request, server.KeyForTest())
			_, requestErr := server.DispatchRaw("/MutateTwoFiles", []byte(body), "s="+reply.Cookie)
			if test.fail && requestErr == nil || !test.fail && requestErr != nil {
				t.Fatalf("request err=%v", requestErr)
			}
			reader := repository
			if test.fail {
				reader, err = accountstate.Open(statePath)
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
			}
			for _, name := range []string{"wallet", "items"} {
				got, err := reader.Load(name)
				if err != nil || string(got) != test.want+name {
					t.Fatalf("%s=%q err=%v", name, got, err)
				}
			}
			if test.fail {
				if _, err := server.DispatchRaw("/MutateTwoFiles", []byte(body), "s="+reply.Cookie); err == nil {
					t.Fatal("server continued after rolling disk back behind published domain memory")
				}
			}
		})
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
