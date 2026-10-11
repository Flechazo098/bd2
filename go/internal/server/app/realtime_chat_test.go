package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	chatpolicy "bd2server/internal/server/domain/chat"
	"bd2server/internal/server/domain/profile"
	"bd2server/internal/server/domain/social"
	"bd2server/internal/server/gateway/realtime"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/room"
	"bd2server/internal/server/storage/shared"
)

type chatTestRooms struct{ calls int }

func (r *chatTestRooms) Handle(context.Context, room.Connection, room.Frame) ([]room.Delivery, error) {
	r.calls++
	return nil, nil
}
func (*chatTestRooms) Disconnect(context.Context, room.Connection) ([]room.Delivery, error) {
	return nil, nil
}

type chatTestText struct{}

func (chatTestText) CheckRoomText(_ context.Context, _ room.Principal, text string) error {
	if text == "rejected" {
		return room.ErrTextRejected
	}
	return nil
}

func newChatTest(t *testing.T) (*realtimeChat, []room.Connection, *time.Time) {
	t.Helper()
	store, err := shared.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	r := &realtimeChat{shared: &sharedRuntime{store: store}, rooms: &chatTestRooms{}, rules: chatTestText{}, historyCount: 2, historyAge: time.Hour, emojis: map[uint64]bool{1: true}, now: func() time.Time { return now }}
	var clients []room.Connection
	for i := 1; i <= 4; i++ {
		clients = append(clients, room.Connection{ID: fmt.Sprint(i), Channel: room.Matching, Principal: room.Principal{AccountID: fmt.Sprint(i), OwnerIndex: int64(i), SessionID: "session", Generation: 1}})
	}
	err = store.Update(context.Background(), func(tx *shared.Tx) error {
		g1 := social.Guild{ID: 1, Members: map[string]social.Membership{}, Reserved: map[string]bool{}}
		g2 := social.Guild{ID: 2, Members: map[string]social.Membership{}, Reserved: map[string]bool{}}
		for _, c := range clients {
			id := uint64(1)
			if c.ID == "4" {
				id = 2
			}
			member := social.Membership{PublicMember: social.PublicMember{AccountID: c.Principal.AccountID, OwnerIndex: uint64(c.Principal.OwnerIndex), Name: "canonical-" + c.ID}}
			if id == 1 {
				g1.Members[c.Principal.AccountID] = member
			} else {
				g2.Members[c.Principal.AccountID] = member
			}
			if err := putChatJSON(tx, "social.accounts", c.Principal.AccountID, social.Account{GuildID: id}); err != nil {
				return err
			}
			p := social.FriendProfile{AccountID: c.Principal.AccountID, OwnerIndex: uint64(c.Principal.OwnerIndex), Version: 1, Snapshot: profile.Snapshot{OwnerIndex: uint64(c.Principal.OwnerIndex), UserID: member.Name, PortraitCostumeID: 42}}
			if err := putChatJSON(tx, "friend.directory", fmt.Sprintf("%020d", c.Principal.OwnerIndex), p); err != nil {
				return err
			}
		}
		if err := putChatJSON(tx, "social.guilds", fmt.Sprintf("%020d", 1), g1); err != nil {
			return err
		}
		return putChatJSON(tx, "social.guilds", fmt.Sprintf("%020d", 2), g2)
	})
	if err != nil {
		t.Fatal(err)
	}
	return r, clients, &now
}
func chatTestFrame(c room.Connection, typ, seq int32, body []byte) room.Frame {
	u := wire.AppendVarint(nil, 1, uint64(c.Principal.OwnerIndex))
	u = wire.AppendVarint(u, 2, 999)
	u = wire.AppendString(u, 4, "forged")
	h := wire.AppendBytes(nil, 3, u)
	return room.Frame{Type: typ, Sequence: seq, OwnerIndex: c.Principal.OwnerIndex, Payload: append(wire.AppendBytes(nil, 1, h), body...)}
}
func chatTestHandle(t *testing.T, r *realtimeChat, c room.Connection, f room.Frame) []room.Delivery {
	t.Helper()
	d, err := r.Handle(context.Background(), c, f)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func chatHistoryRows(raw []byte) [][]byte {
	var rows [][]byte
	_ = wire.Walk(raw, func(f wire.Field) error {
		if f.Number == 2 {
			rows = append(rows, f.Value)
		}
		return nil
	})
	return rows
}

// Packet numbers and field layouts are from 2.36.7 Network/TCP ENetMsg,
// CommonHeader, CSGuildMessageLoadResponse and SCGuildBroadcastRequest.
func TestNativeGuildChatCanonicalHandshakeAudienceAndBlockHistory(t *testing.T) {
	r, clients, _ := newChatTest(t)
	for _, c := range clients {
		d := chatTestHandle(t, r, c, chatTestFrame(c, 11, 1, nil))
		u := chatBytes(chatBytes(d[0].Frame.Payload, 1), 3)
		guild := uint64(1)
		if c.ID == "4" {
			guild = 2
		}
		if d[0].Frame.Type != 12 || string(chatBytes(u, 4)) != "canonical-"+c.ID || chatScalar(u, 2) != guild {
			t.Fatal("handshake trusted forged profile or omitted required UserInfo")
		}
	}
	policy := chatpolicy.Policy{AccountID: "2", OwnerIndex: 2, Version: 2, Blocked: []uint64{1}}
	if err := r.shared.store.Update(context.Background(), func(tx *shared.Tx) error { return putChatJSON(tx, "chat.policy", "2", policy) }); err != nil {
		t.Fatal(err)
	}
	body := wire.AppendBytes(nil, 3, wire.AppendString(nil, 1, "hello"))
	d := chatTestHandle(t, r, clients[0], chatTestFrame(clients[0], 15, 2, body))
	got := map[string]bool{}
	for _, delivery := range d {
		if delivery.Frame.Type == 17 {
			got[delivery.ConnectionID] = true
			u := chatBytes(chatBytes(delivery.Frame.Payload, 1), 3)
			if string(chatBytes(u, 4)) != "canonical-1" {
				t.Fatal("broadcast trusted client name")
			}
		}
	}
	if len(got) != 2 || !got["1"] || !got["3"] {
		t.Fatalf("guild/block audience = %v", got)
	}
	for _, i := range []int{1, 2, 3} {
		loaded := chatTestHandle(t, r, clients[i], chatTestFrame(clients[i], 13, 3, nil))
		rows := chatHistoryRows(loaded[0].Frame.Payload)
		want := 0
		if i == 2 {
			want = 1
		}
		if len(rows) != want {
			t.Fatalf("client %s history=%d want%d", clients[i].ID, len(rows), want)
		}
	}
	if _, err := r.Handle(context.Background(), clients[0], room.Frame{Type: 78}); err != nil {
		t.Fatal(err)
	}
	if r.rooms.(*chatTestRooms).calls != 1 {
		t.Fatal("matchmaking did not reach existing room handler")
	}
}

func TestNativeGuildChatReplayReconnectMembershipAndRetention(t *testing.T) {
	r, clients, now := newChatTest(t)
	for _, c := range clients {
		chatTestHandle(t, r, c, chatTestFrame(c, 11, 1, nil))
	}
	body := wire.AppendVarint(nil, 2, 1)
	body = wire.AppendBytes(body, 4, wire.AppendVarint(nil, 1, 1))
	f := chatTestFrame(clients[0], 15, 2, body)
	chatTestHandle(t, r, clients[0], f)
	reconnected := clients[0]
	reconnected.ID = "new"
	chatTestHandle(t, r, reconnected, chatTestFrame(reconnected, 11, 3, nil))
	if _, err := r.Disconnect(context.Background(), clients[0]); err != nil {
		t.Fatal(err)
	}
	replay := chatTestHandle(t, r, reconnected, f)
	if len(replay) != 1 || replay[0].Frame.Type != 16 {
		t.Fatal("replay rebroadcast durable message")
	}
	if err := r.shared.store.Update(context.Background(), func(tx *shared.Tx) error {
		var g social.Guild
		if _, err := chatJSON(tx, "social.guilds", fmt.Sprintf("%020d", 1), &g); err != nil {
			return err
		}
		delete(g.Members, "3")
		if err := putChatJSON(tx, "social.guilds", fmt.Sprintf("%020d", 1), g); err != nil {
			return err
		}
		return putChatJSON(tx, "social.accounts", "3", social.Account{})
	}); err != nil {
		t.Fatal(err)
	}
	for seq := int32(4); seq <= 5; seq++ {
		d := chatTestHandle(t, r, reconnected, chatTestFrame(reconnected, 15, seq, body))
		for _, delivery := range d {
			if delivery.Frame.Type == 17 && delivery.ConnectionID == "3" {
				t.Fatal("removed member received guild chat")
			}
		}
	}
	loaded := chatTestHandle(t, r, reconnected, chatTestFrame(reconnected, 13, 6, nil))
	rows := chatHistoryRows(loaded[0].Frame.Payload)
	if len(rows) != 2 || chatScalar(rows[0], 7) >= chatScalar(rows[1], 7) {
		t.Fatal("history not bounded or native millisecond message timestamps collide")
	}
	*now = now.Add(2 * time.Hour)
	loaded = chatTestHandle(t, r, reconnected, chatTestFrame(reconnected, 13, 7, nil))
	if len(chatHistoryRows(loaded[0].Frame.Payload)) != 0 {
		t.Fatal("expired history returned")
	}
	denied := chatTestHandle(t, r, clients[2], chatTestFrame(clients[2], 15, 8, body))
	if len(denied) != 1 || chatScalar(chatBytes(denied[0].Frame.Payload, 1), 2) != 3 {
		t.Fatal("removed member could send")
	}
}

func TestNativeGuildChatRejectsTextAndMalformedNamedDeckWithoutDisconnect(t *testing.T) {
	r, clients, _ := newChatTest(t)
	c := clients[0]
	chatTestHandle(t, r, c, chatTestFrame(c, 11, 1, nil))
	for i, body := range [][]byte{wire.AppendBytes(nil, 3, wire.AppendString(nil, 1, "rejected")), append(wire.AppendVarint(nil, 2, 2), wire.AppendBytes(nil, 5, wire.AppendBytes(wire.AppendString(nil, 2, "named"), 1, nil))...)} {
		d := chatTestHandle(t, r, c, chatTestFrame(c, 15, int32(i+2), body))
		if len(d) != 1 || chatScalar(chatBytes(d[0].Frame.Payload, 1), 2) != 2 {
			t.Fatal("rejected text or null deck character broadcast")
		}
	}
	ping := chatTestHandle(t, r, c, chatTestFrame(c, 1, 5, nil))
	if ping[0].Frame.Type != 2 {
		t.Fatal("invalid chat closed healthy socket")
	}
}

type chatSocketTickets struct{ principal room.Principal }

func (t chatSocketTickets) Consume(context.Context, string, string, string) (room.Principal, func(), error) {
	return t.principal, func() {}, nil
}
func (chatSocketTickets) Validate(context.Context, room.Principal) error { return nil }

func TestNativeGuildChatSocketHandshakeAndIdleHeartbeatsStayConnected(t *testing.T) {
	r, clients, _ := newChatTest(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	certFile, keyFile, pool := chatSocketCertificate(t)
	server, err := realtime.New(realtime.Config{TCPListen: address, TLSCertFile: certFile, TLSKeyFile: keyFile, IdleTimeout: time.Second}, r, chatSocketTickets{principal: clients[0].Principal})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := server.Close(context.Background()); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	var socket net.Conn
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		socket, err = tls.DialWithDialer(&net.Dialer{Timeout: 100 * time.Millisecond}, "tcp", address, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := socket.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := socket.SetDeadline(time.Now().Add(6 * time.Second)); err != nil {
		t.Fatal(err)
	}
	auth, err := json.Marshal(realtime.AuthRequest{Ticket: "test-ticket", Channel: "matching", InstanceID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	header := append([]byte(realtime.AuthMagic), 0, 0)
	binary.LittleEndian.PutUint16(header[8:], uint16(len(auth)))
	if _, err := socket.Write(append(header, auth...)); err != nil {
		t.Fatal(err)
	}
	var ack [10]byte
	if _, err := io.ReadFull(socket, ack[:]); err != nil {
		t.Fatal(err)
	}
	if string(ack[:8]) != realtime.AckMagic {
		t.Fatal("authenticated transport handshake failed")
	}
	ackBody := make([]byte, binary.LittleEndian.Uint16(ack[8:]))
	if _, err := io.ReadFull(socket, ackBody); err != nil {
		t.Fatal(err)
	}
	f := chatTestFrame(clients[0], 11, 1, nil)
	if _, err := socket.Write(realtime.EncodeFrame(f)); err != nil {
		t.Fatal(err)
	}
	response, err := realtime.ReadFrame(socket, 4096)
	if err != nil {
		t.Fatal(err)
	}
	user := chatBytes(chatBytes(response.Payload, 1), 3)
	if response.Type != 12 || chatScalar(user, 1) != 1 || string(chatBytes(user, 4)) != "canonical-1" {
		t.Fatal("native login response omitted canonical UserInfo")
	}
	for sequence := int32(2); sequence < 14; sequence++ {
		time.Sleep(100 * time.Millisecond)
		if _, err := socket.Write(realtime.EncodeFrame(chatTestFrame(clients[0], 1, sequence, nil))); err != nil {
			t.Fatal(err)
		}
		response, err := realtime.ReadFrame(socket, 4096)
		if err != nil {
			t.Fatalf("healthy heartbeat connection dropped: %v", err)
		}
		if response.Type != 2 || response.Sequence != sequence || chatScalar(chatBytes(response.Payload, 1), 2) != 0 {
			t.Fatal("native heartbeat response invalid")
		}
	}
}

func chatSocketCertificate(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("test trust root invalid")
	}
	return certPath, keyPath, pool
}
