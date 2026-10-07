package session_test

import (
	"bd2server/internal/server/gateway/session"
	"bd2server/internal/server/gateway/transport"
	"bd2server/internal/server/runtime/player"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This client implements the documented base64/protobuf/AES transport itself.
// It obtains each key from LoginUser, never through an internal test key API.
func clientEncrypt(t *testing.T, plain, key []byte, payload bool) []byte {
	t.Helper()
	if payload {
		plain = []byte(base64.StdEncoding.EncodeToString(plain))
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		t.Fatal(e)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(out, plain)
	return []byte(base64.StdEncoding.EncodeToString(out))
}
func clientDecrypt(t *testing.T, encoded string, key []byte) []byte {
	t.Helper()
	raw, e := base64.StdEncoding.DecodeString(encoded)
	if e != nil {
		t.Fatal(e)
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		t.Fatal(e)
	}
	plain := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(plain, raw)
	pad := int(plain[len(plain)-1])
	plain = plain[:len(plain)-pad]
	decoded, e := base64.StdEncoding.DecodeString(string(plain))
	if e != nil {
		t.Fatal(e)
	}
	return decoded
}
func scalar(n int, v uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, uint64(n<<3)), v)
}
func message(n int, b []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(n<<3|2))
	out = binary.AppendUvarint(out, uint64(len(b)))
	return append(out, b...)
}
func field(proto []byte, wanted int) []byte {
	for len(proto) > 0 {
		k, n := binary.Uvarint(proto)
		if n <= 0 {
			return nil
		}
		proto = proto[n:]
		if k&7 == 0 {
			_, n = binary.Uvarint(proto)
			if n <= 0 {
				return nil
			}
			proto = proto[n:]
			continue
		}
		if k&7 != 2 {
			return nil
		}
		size, n := binary.Uvarint(proto)
		if n <= 0 || size > uint64(len(proto)-n) {
			return nil
		}
		value := proto[n : n+int(size)]
		if int(k>>3) == wanted {
			return value
		}
		proto = proto[n+int(size):]
	}
	return nil
}

type authenticator struct{}

func (authenticator) AuthenticateLogin(p []byte) (string, error) {
	id := string(field(p, 2))
	if id != "A" && id != "B" {
		return "", errors.New("unknown identity")
	}
	return id, nil
}

type executor struct {
	account    string
	blocked    chan struct{}
	release    chan struct{}
	once       sync.Once
	executions atomic.Int32
}

func (e *executor) Recover(context.Context) error { return nil }

func (e *executor) Execute(ctx context.Context, c player.Command) (player.Reply, error) {
	if c.Requests[0].Path == "/LoginUser" {
		user := message(3, c.LoginSessionKey)
		return player.Reply{Responses: []player.Response{{PacketCode: 3, Body: message(1, user)}}}, nil
	}
	if e.account == "A" {
		e.once.Do(func() { close(e.blocked); <-e.release })
	}
	if ctx.Err() != nil {
		return player.Reply{}, ctx.Err()
	}
	e.executions.Add(1)
	var reply player.Reply
	for _, r := range c.Requests {
		reply.Responses = append(reply.Responses, player.Response{PacketCode: 1, Body: message(1, []byte(e.account+r.Path)), Notification: message(1, []byte(r.Path))})
	}
	return reply, nil
}

type router map[string]*player.Runtime

func (r router) Acquire(_ context.Context, id string) (*player.Runtime, func(), error) {
	owner := r[id]
	if owner == nil {
		return nil, nil, errors.New("unknown player")
	}
	return owner, func() {}, nil
}
func TestCancelledClientRetryAndTwoAccountsDoNotShareExecution(t *testing.T) {
	a := &executor{account: "A", blocked: make(chan struct{}), release: make(chan struct{})}
	b := &executor{account: "B"}
	owners := router{}
	for id, e := range map[string]*executor{"A": a, "B": b} {
		owner, err := player.New(id, e, player.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		owners[id] = owner
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = owner.Close(ctx)
		}()
	}
	server, err := session.NewServer(owners, authenticator{})
	if err != nil {
		t.Fatal(err)
	}
	fixed := []byte("abcdefghijkrstuv024680wxyzlmnopq")
	login := func(id string) (string, []byte) {
		t.Helper()
		request := append(scalar(1, 1), message(2, []byte(id))...)
		reply, err := server.DispatchRaw(context.Background(), "/LoginUser", clientEncrypt(t, request, fixed, true), "")
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct{ Data string }
		if err = json.Unmarshal(reply.Body, &envelope); err != nil {
			t.Fatal(err)
		}
		key := field(field(clientDecrypt(t, envelope.Data, fixed), 1), 3)
		if len(key) != 32 {
			t.Fatal("client received invalid login key")
		}
		return "s=" + reply.Cookie, key
	}
	aCookie, aKey := login("A")
	bCookie, bKey := login("B")
	if bytes.Equal(aKey, bKey) {
		t.Fatal("accounts share encryption key")
	}
	request := scalar(1, 2)
	aBody := clientEncrypt(t, request, aKey, true)
	ctx, cancel := context.WithCancel(context.Background())
	pending := make(chan error, 1)
	go func() { _, e := server.DispatchRaw(ctx, "/ItemInfo", aBody, aCookie); pending <- e }()
	select {
	case <-a.blocked:
	case <-time.After(time.Second):
		t.Fatal("A did not enter owner")
	}
	bDone := make(chan error, 1)
	go func() {
		_, e := server.DispatchRaw(context.Background(), "/ItemInfo", clientEncrypt(t, request, bKey, true), bCookie)
		bDone <- e
	}()
	select {
	case e := <-bDone:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked A serialized unrelated B")
	}
	cancel()
	select {
	case e := <-pending:
		if !errors.Is(e, context.Canceled) {
			t.Fatal("HTTP cancellation not propagated", e)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled client stayed blocked")
	}
	close(a.release)
	if _, err = server.DispatchRaw(context.Background(), "/ItemInfo", aBody, aCookie); err != nil {
		t.Fatal(err)
	}
	if a.executions.Load() != 1 || b.executions.Load() != 1 {
		t.Fatal("retry duplicated accepted operation")
	}
	changed := clientEncrypt(t, append(request, scalar(2, 99)...), aKey, true)
	if _, err = server.DispatchRaw(context.Background(), "/ItemInfo", changed, aCookie); !errors.Is(err, player.ErrIdentityConflict) {
		t.Fatal("same operation identity accepted modified asset request", err)
	}
	if _, err = server.DispatchRaw(context.Background(), "/ItemInfo", aBody, bCookie); err == nil {
		t.Fatal("one player's ciphertext accepted with another cookie")
	}
	// A restored actor has no unfinished battle/preview state. Old encrypted
	// traffic must reconnect instead of interpreting a cached transient reply.
	if err := owners["B"].Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = server.DispatchRaw(context.Background(), "/ItemInfo", clientEncrypt(t, scalar(1, 8), bKey, true), bCookie); !errors.Is(err, transport.ErrGameSessionExpired) {
		t.Fatal("recovered owner accepted old game session", err)
	}
	bCookie, bKey = login("B")
	oldCookie, oldKey := bCookie, bKey
	bCookie, bKey = login("B")
	if _, err = server.DispatchRaw(context.Background(), "/ItemInfo", clientEncrypt(t, scalar(1, 9), oldKey, true), oldCookie); !errors.Is(err, transport.ErrGameSessionExpired) {
		t.Fatal("superseded login retained mutable player session", err)
	}
	if _, err = server.DispatchRaw(context.Background(), "/ItemInfo", clientEncrypt(t, scalar(1, 9), bKey, true), bCookie); err != nil {
		t.Fatal("new game login rejected", err)
	}
	if err := owners["B"].Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	replacement, err := player.New("B", b, player.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacement.Close(context.Background()) }()
	owners["B"] = replacement
	if _, err = server.DispatchRaw(context.Background(), "/ItemInfo", clientEncrypt(t, scalar(1, 10), bKey, true), bCookie); !errors.Is(err, transport.ErrGameSessionExpired) {
		t.Fatal("unloaded actor's key reused by fresh state generation", err)
	}

}
