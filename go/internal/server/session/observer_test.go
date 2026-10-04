package session

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/cryptox"
	"bd2server/internal/server/protocol"
	"bd2server/internal/server/wire"
)

type testProgressObserver struct {
	store   *accountstate.Repository
	fail    bool
	session string
}

func (o *testProgressObserver) BeginSession(id string)              { o.session = id }
func (o *testProgressObserver) BeforeDispatch(string, []byte) error { return nil }
func (o *testProgressObserver) AfterDispatch(path string, request, _ []byte) ([]byte, error) {
	if o.session == "" {
		return nil, errors.New("observer missing session")
	}
	seq, _, _ := wire.Varint(request, 1)
	if err := o.store.PutEntry("missions", "test_observation", path, []byte{byte(seq)}); err != nil {
		return nil, err
	}
	if o.fail {
		return nil, errors.New("observation failed after write")
	}
	return wire.AppendBytes(nil, 2, wire.AppendVarint(nil, 2, seq)), nil
}

func TestObserverNotificationsAndFailuresShareRequestTransaction(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	repository, err := accountstate.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	server, _ := NewServer(fakeLogin{}, fakeDomain{}, &mutatingDomain{store: repository})
	server.AttachStateStore(repository)
	observer := &testProgressObserver{store: repository}
	server.AttachResponseObserver(observer)
	logged := login(t, server)
	requests := []protocol.BatchRequest{
		{Path: "/EmptyInfo", RequestData: base64.StdEncoding.EncodeToString(wire.AppendVarint(nil, 1, 2))},
		{Path: "/MutateTwoFiles", RequestData: base64.StdEncoding.EncodeToString(wire.AppendVarint(nil, 1, 3))},
	}
	plain, _ := json.Marshal(requests)
	body, _ := cryptox.EncryptBase64(plain, server.KeyForTest())
	reply, err := server.DispatchRaw("/BatchRequest", []byte(body), "s="+logged.Cookie)
	if err != nil {
		t.Fatal(err)
	}
	var results []protocol.BatchResponse
	if err := json.Unmarshal(reply.Body, &results); err != nil {
		t.Fatal(err)
	}
	for i, result := range results {
		notify, err := base64.StdEncoding.DecodeString(result.ResponseData.Notify)
		row, _, _ := wire.Bytes(notify, 2)
		value, _, _ := wire.Varint(row, 2)
		if err != nil || value != uint64(i+2) {
			t.Fatalf("batch notify value=%d index=%d err=%v", value, i, err)
		}
	}
	observer.fail = true
	body, _ = cryptox.EncryptBase64Payload(wire.AppendVarint(nil, 1, 4), server.KeyForTest())
	if _, err := server.DispatchRaw("/MutateTwoFiles", []byte(body), "s="+logged.Cookie); err == nil {
		t.Fatal("observer failure accepted")
	}
	verified, err := sql.Open("sqlite", statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer verified.Close()
	var value []byte
	err = verified.QueryRow("SELECT payload FROM domain_entry WHERE domain_name='missions' AND bucket='test_observation' AND entry_key='/MutateTwoFiles'").Scan(&value)
	if err != nil || len(value) != 1 || value[0] != 3 {
		t.Fatalf("rolled back observer value=%v err=%v", value, err)
	}
}
