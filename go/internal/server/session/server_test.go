package session

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/cryptox"
	"bd2server/internal/server/protocol"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/transport"
	"bd2server/internal/server/wire"
)

type fakeLogin struct{}

func (fakeLogin) Login(request, key []byte) ([]byte, error) {
	if _, ok, err := wire.Varint(request, 1); err != nil || !ok {
		return nil, errors.New("missing seq")
	}
	user := wire.AppendBytes(nil, 3, key)
	return wire.AppendBytes(nil, 1, user), nil
}

type fakeAuthenticator struct {
	accountID string
	err       error
	calls     int
	request   []byte
}

func (a *fakeAuthenticator) AuthenticateLogin(request []byte) (string, error) {
	a.calls++
	a.request = append([]byte(nil), request...)
	return a.accountID, a.err
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
	verified, err := sql.Open("sqlite", statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer verified.Close()
	for name, want := range map[string]string{"wallet": "old-wallet", "items": "old-items"} {
		var got []byte
		err := verified.QueryRow(`SELECT payload FROM domain_state WHERE name=?`, name).Scan(&got)
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
	return loginRequest(t, server, request)
}

func loginRequest(t *testing.T, server *Server, request []byte) transport.RawReply {
	t.Helper()
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

func TestLoginAuthenticatesBeforeCreatingGameSession(t *testing.T) {
	server, err := NewServer(fakeLogin{})
	if err != nil {
		t.Fatal(err)
	}
	authenticator := &fakeAuthenticator{accountID: "account-1"}
	if err := server.AttachLoginAuthenticator(authenticator); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendString(request, 2, "short-lived-access-token")
	reply := loginRequest(t, server, request)
	if authenticator.calls != 1 {
		t.Fatalf("AuthenticateLogin calls=%d, want 1", authenticator.calls)
	}
	accessToken, found, err := wire.Bytes(authenticator.request, 2)
	if err != nil || !found || string(accessToken) != "short-lived-access-token" {
		t.Fatalf("authenticated access token=%q found=%v err=%v", accessToken, found, err)
	}
	game := server.sessions[sessionTokenKey(reply.Cookie)]
	if game == nil || game.accountID != "account-1" {
		t.Fatalf("game session=%+v, want authenticated account", game)
	}
}

func TestLoginAuthenticationFailureCreatesNoSession(t *testing.T) {
	server, err := NewServer(fakeLogin{})
	if err != nil {
		t.Fatal(err)
	}
	authenticator := &fakeAuthenticator{err: errors.New("invalid access token")}
	if err := server.AttachLoginAuthenticator(authenticator); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	body, err := cryptox.EncryptBase64Payload(request, cryptox.Key())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.DispatchRaw("/LoginUser", []byte(body), ""); err == nil {
		t.Fatal("invalid access token was accepted")
	}
	if authenticator.calls != 1 || len(server.sessions) != 0 {
		t.Fatalf("auth calls=%d sessions=%d", authenticator.calls, len(server.sessions))
	}

	authenticator.err = nil
	authenticator.accountID = ""
	if _, err := server.DispatchRaw("/LoginUser", []byte(body), ""); err == nil {
		t.Fatal("empty authenticated account ID was accepted")
	}
	if len(server.sessions) != 0 {
		t.Fatalf("empty account ID created %d sessions", len(server.sessions))
	}
}

func TestGameSessionsHaveIndependentKeysAndCookies(t *testing.T) {
	server, err := NewServer(fakeLogin{}, fakeDomain{})
	if err != nil {
		t.Fatal(err)
	}
	first := login(t, server)
	firstKey := server.KeyForTest(first.Cookie)
	second := login(t, server)
	secondKey := server.KeyForTest(second.Cookie)
	if first.Cookie == second.Cookie {
		t.Fatal("two logins received the same game session cookie")
	}
	if string(firstKey) == string(secondKey) {
		t.Fatal("two logins received the same game session key")
	}

	request := wire.AppendVarint(nil, 1, 2)
	firstBody, err := cryptox.EncryptBase64Payload(request, firstKey)
	if err != nil {
		t.Fatal(err)
	}
	firstReply, err := server.DispatchRaw("/EmptyInfo", []byte(firstBody), "other=value; s="+first.Cookie)
	if err != nil {
		t.Fatalf("first session stopped working after second login: %v", err)
	}
	var firstEnvelope protocol.Envelope
	if err := json.Unmarshal(firstReply.Body, &firstEnvelope); err != nil {
		t.Fatal(err)
	}
	if _, err := cryptox.DecryptBase64Payload(firstEnvelope.Data, firstKey); err != nil {
		t.Fatalf("first response did not use first key: %v", err)
	}

	secondBody, err := cryptox.EncryptBase64Payload(request, secondKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.DispatchRaw("/EmptyInfo", []byte(secondBody), "s="+second.Cookie); err != nil {
		t.Fatalf("second session request failed: %v", err)
	}
	if _, err := server.DispatchRaw("/EmptyInfo", []byte(firstBody), "s="+second.Cookie); err == nil {
		t.Fatal("request encrypted with first key was accepted under second cookie")
	}
	if _, err := server.DispatchRaw("/EmptyInfo", []byte(secondBody), "s="+first.Cookie); err == nil {
		t.Fatal("request encrypted with second key was accepted under first cookie")
	}
	if _, err := server.DispatchRaw("/EmptyInfo", []byte(firstBody), "s="+first.Cookie+"; s="+second.Cookie); err == nil {
		t.Fatal("ambiguous duplicate session cookies were accepted")
	}
}

func TestGameSessionExpiresAndClearsKey(t *testing.T) {
	server, err := NewServer(fakeLogin{}, fakeDomain{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }
	server.sessionTTL = time.Minute
	reply := login(t, server)
	game := server.sessions[sessionTokenKey(reply.Cookie)]
	key := append([]byte(nil), game.key...)
	now = now.Add(time.Minute)
	request := wire.AppendVarint(nil, 1, 2)
	body, err := cryptox.EncryptBase64Payload(request, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.DispatchRaw("/EmptyInfo", []byte(body), "s="+reply.Cookie); err == nil {
		t.Fatal("expired game session was accepted")
	} else if !errors.Is(err, transport.ErrGameSessionExpired) {
		t.Fatalf("expired game session error=%v, want ErrGameSessionExpired", err)
	}
	if len(server.sessions) != 0 {
		t.Fatalf("expired game session remains in map: %d", len(server.sessions))
	}
	for i, value := range game.key {
		if value != 0 {
			t.Fatalf("expired session key byte %d was not cleared", i)
		}
	}
}

func TestGameSessionLimitEvictsLeastRecentlyUsedAndClearsKey(t *testing.T) {
	server, err := NewServer(fakeLogin{}, fakeDomain{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }
	server.maxSessions = 2
	first := login(t, server)
	firstKey := server.KeyForTest(first.Cookie)
	now = now.Add(time.Second)
	second := login(t, server)
	secondGame := server.sessions[sessionTokenKey(second.Cookie)]

	now = now.Add(time.Second)
	request := wire.AppendVarint(nil, 1, 2)
	firstBody, err := cryptox.EncryptBase64Payload(request, firstKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.DispatchRaw("/EmptyInfo", []byte(firstBody), "s="+first.Cookie); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	third := login(t, server)
	if len(server.sessions) != 2 {
		t.Fatalf("game sessions=%d, want cap 2", len(server.sessions))
	}
	if _, ok := server.sessions[sessionTokenKey(first.Cookie)]; !ok {
		t.Fatal("recently used first session was evicted")
	}
	if _, ok := server.sessions[sessionTokenKey(third.Cookie)]; !ok {
		t.Fatal("new third session is missing")
	}
	if _, ok := server.sessions[sessionTokenKey(second.Cookie)]; ok {
		t.Fatal("least recently used second session was not evicted")
	}
	for i, value := range secondGame.key {
		if value != 0 {
			t.Fatalf("evicted session key byte %d was not cleared", i)
		}
	}
}

func TestSessionCookieParsingIsStrict(t *testing.T) {
	valid := strings.Repeat("a", 48) + "|1"
	if token, err := parseSessionCookie("other=value; s=" + valid); err != nil || token != valid {
		t.Fatalf("valid cookie token=%q err=%v", token, err)
	}
	for name, cookie := range map[string]string{
		"missing":       "other=value",
		"empty":         "s=",
		"wrong length":  "s=abcd|1",
		"uppercase":     "s=" + strings.Repeat("A", 48) + "|1",
		"wrong version": "s=" + strings.Repeat("a", 48) + "|2",
		"duplicate":     "s=" + valid + "; s=" + valid,
		"oversized":     "x=" + strings.Repeat("a", maxCookieHeaderLen),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSessionCookie(cookie); err == nil {
				t.Fatalf("accepted malformed cookie %q", cookie)
			}
		})
	}
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
	if _, err := server.DispatchRaw("/EmptyInfo", nil, ""); !errors.Is(err, transport.ErrGameSessionExpired) {
		t.Fatalf("missing cookie error=%v, want ErrGameSessionExpired", err)
	}
	unknown := strings.Repeat("a", 48) + "|1"
	if _, err := server.DispatchRaw("/BatchRequest", nil, "s="+unknown); !errors.Is(err, transport.ErrGameSessionExpired) {
		t.Fatalf("unknown cookie error=%v, want ErrGameSessionExpired", err)
	}
	if _, err := server.DispatchRaw("/EmptyInfo", nil, "s=malformed"); err == nil ||
		errors.Is(err, transport.ErrGameSessionExpired) {
		t.Fatalf("malformed cookie error=%v, want ordinary rejection", err)
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
			for _, name := range []string{"wallet", "items"} {
				var got []byte
				if test.fail {
					reader, openErr := sql.Open("sqlite", statePath)
					if openErr != nil {
						t.Fatal(openErr)
					}
					err = reader.QueryRow(`SELECT payload FROM domain_state WHERE name=?`, name).Scan(&got)
					reader.Close()
				} else {
					got, err = repository.Load(name)
				}
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
