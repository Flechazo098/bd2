package realtime

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/room"
)

type integrationRules struct{}

func (integrationRules) Profile(_ context.Context, p room.Principal) (room.Profile, error) {
	return room.Profile{UserInfo: wire.AppendVarint(nil, 1, uint64(p.OwnerIndex))}, nil
}
func (integrationRules) ValidateSelection(_ room.Profile, s room.Selection) (room.Selection, error) {
	s.Capacity = 1
	s.MonsterID = 104
	return s, nil
}

type integrationTickets struct {
	mu       sync.Mutex
	used     map[string]bool
	p        room.Principal
	instance string
}

func (t *integrationTickets) Validate(context.Context, room.Principal) error { return nil }
func (t *integrationTickets) Consume(_ context.Context, token, channel, instance string) (room.Principal, func(), error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if token != channel+"-ticket" || t.used[token] || channel == "action" && instance != t.instance {
		return room.Principal{}, nil, errors.New("invalid ticket")
	}
	t.used[token] = true
	return t.p, func() {}, nil
}

func TestNativeTCPHandshakeMatchingAndEntry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan room.FinalResult, 1)
	var s *Server
	m, err := room.New(room.Config{AdvertisedHost: "127.0.0.1", TCPPort: 9000,
		PublishResult: func(result room.FinalResult) bool { results <- result; return true },
		Publish:       func(d []room.Delivery) { s.Deliver(d) },
	}, integrationRules{})
	if err != nil {
		t.Fatal(err)
	}
	tickets := &integrationTickets{p: room.Principal{AccountID: "account", OwnerIndex: 91, SessionID: "session", Generation: 1}, used: map[string]bool{}}
	s, err = New(Config{TCPListen: "127.0.0.1:0", DevelopmentPlainLoopback: true}, m, tickets)
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelClose()
		if e := s.Close(closeCtx); e != nil {
			t.Error(e)
		}
		if e := m.Close(closeCtx); e != nil {
			t.Error(e)
		}
		select {
		case e := <-serveDone:
			if e != nil {
				t.Error(e)
			}
		case <-closeCtx.Done():
			t.Error("listener did not stop")
		}
	})
	var address string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		if s.listener != nil {
			address = s.listener.Addr().String()
		}
		s.mu.Unlock()
		if address != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if address == "" {
		t.Fatal("listener did not start")
	}
	connect := func(channel, instance string) net.Conn {
		t.Helper()
		socket, e := net.DialTimeout("tcp", address, time.Second)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			if e := socket.Close(); e != nil {
				t.Error(e)
			}
		})
		if e = socket.SetDeadline(time.Now().Add(3 * time.Second)); e != nil {
			t.Fatal(e)
		}
		b, _ := json.Marshal(AuthRequest{Ticket: channel + "-ticket", Channel: channel, InstanceID: instance})
		prefix := append([]byte(AuthMagic), byte(len(b)), byte(len(b)>>8))
		if e = writeAll(socket, append(prefix, b...)); e != nil {
			t.Fatal(e)
		}
		var h [10]byte
		if _, e = io.ReadFull(socket, h[:]); e != nil {
			t.Fatal(e)
		}
		if string(h[:8]) != AckMagic {
			t.Fatal("missing authenticated acknowledgment")
		}
		ack := make([]byte, binary.LittleEndian.Uint16(h[8:]))
		if _, e = io.ReadFull(socket, ack); e != nil {
			t.Fatal(e)
		}
		var a AuthAck
		if e = json.Unmarshal(ack, &a); e != nil || a.ConnectionID == "" || a.Epoch == 0 {
			t.Fatalf("invalid ack %+v %v", a, e)
		}
		if e = writeAll(socket, EncodeFrame(room.Frame{Type: 11, Sequence: 1, OwnerIndex: 91})); e != nil {
			t.Fatal(e)
		}
		r, e := ReadFrame(socket, 4096)
		if e != nil || r.Type != 12 || r.Sequence != 1 {
			t.Fatalf("native startup failed %+v %v", r, e)
		}
		return socket
	}
	matching := connect("matching", "boot")
	request := wire.AppendVarint(wire.AppendVarint(nil, 4, 4), 5, 1)
	if err = writeAll(matching, EncodeFrame(room.Frame{Type: 82, Sequence: 2, OwnerIndex: 91, Payload: request})); err != nil {
		t.Fatal(err)
	}
	r, err := ReadFrame(matching, 4096)
	if err != nil || r.Type != 83 || r.Sequence != 2 {
		t.Fatalf("matching reply %+v %v", r, err)
	}
	r, err = ReadFrame(matching, 4096)
	if err != nil || r.Type != 101 {
		t.Fatalf("matching completion %+v %v", r, err)
	}
	code, _, err := wire.Bytes(r.Payload, 2)
	if err != nil || len(code) == 0 {
		t.Fatal("completion omitted room code")
	}
	view, err := m.View(ctx, tickets.p, room.Action)
	if err != nil {
		t.Fatal(err)
	}
	tickets.mu.Lock()
	tickets.instance = view.InstanceID
	tickets.mu.Unlock()
	game := connect("action", view.InstanceID)
	if err = writeAll(game, EncodeFrame(room.Frame{Type: 23, Sequence: 3, OwnerIndex: 91, Payload: wire.AppendBytes(nil, 2, code)})); err != nil {
		t.Fatal(err)
	}
	r, err = ReadFrame(game, 4096)
	if err != nil || r.Type != 24 {
		t.Fatalf("entry reply %+v %v", r, err)
	}
	guid, _, err := wire.Bytes(r.Payload, 4)
	if err != nil || len(guid) == 0 {
		t.Fatal("entry omitted GUID")
	}
	if err = writeAll(game, EncodeFrame(room.Frame{Type: 45, Sequence: 4, OwnerIndex: 91, Payload: wire.AppendBytes(nil, 2, guid)})); err != nil {
		t.Fatal(err)
	}
	r, err = ReadFrame(game, 4096)
	if err != nil || r.Type != 46 {
		t.Fatalf("ready reply %+v %v", r, err)
	}
	r, err = ReadFrame(game, 4096)
	if err != nil || r.Type != 106 {
		t.Fatalf("game did not start %+v %v", r, err)
	}
	if err = m.AcceptRun(ctx, tickets.p, room.Action, view.InstanceID, string(guid)); err != nil {
		t.Fatal(err)
	}
	monster := wire.AppendVarint(nil, 3, 104)
	if err = writeAll(game, EncodeFrame(room.Frame{Type: 111, Sequence: 5, OwnerIndex: 91, Payload: wire.AppendBytes(wire.AppendBytes(nil, 2, guid), 5, monster)})); err != nil {
		t.Fatal(err)
	}
	var final room.FinalResult
	select {
	case final = <-results:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-time.After(3 * time.Second):
		t.Fatal("terminal result was not queued")
	}
	if err = game.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, err = ReadFrame(game, 4096)
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("terminal frame arrived before durable acknowledgment: %v", err)
	}
	if err = game.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = m.AcknowledgeResult(ctx, final.View.InstanceID); err != nil {
		t.Fatal(err)
	}
	r, err = ReadFrame(game, 4096)
	if err != nil || r.Type != 108 {
		t.Fatalf("durable terminal notification missing: %+v %v", r, err)
	}
	if err = writeAll(game, EncodeFrame(room.Frame{Type: 66, Sequence: 5, OwnerIndex: 999, Payload: wire.AppendBytes(nil, 2, guid)})); err != nil {
		t.Fatal(err)
	}
	_, err = ReadFrame(game, 4096)
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		t.Fatal("forged owner header left the authenticated connection open until timeout")
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("forged owner header did not close the authenticated connection: %v", err)
	}
}

type blockedUDPRooms struct {
	entered   chan struct{}
	release   chan struct{}
	processed chan udpProcessed
}

type udpProcessed struct {
	connection string
	sequence   int32
}

func (r *blockedUDPRooms) Handle(ctx context.Context, c room.Connection, f room.Frame) ([]room.Delivery, error) {
	if c.ID == "slow" && f.Sequence == 1 {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	r.processed <- udpProcessed{connection: c.ID, sequence: f.Sequence}
	return nil, nil
}
func (*blockedUDPRooms) Disconnect(context.Context, room.Connection) ([]room.Delivery, error) {
	return nil, nil
}

func TestUDPDispatchIsolatesBlockedConnectionsAndPreservesOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rooms := &blockedUDPRooms{entered: make(chan struct{}), release: make(chan struct{}), processed: make(chan udpProcessed, 3)}
	s, err := New(Config{TCPListen: "127.0.0.1:0", DevelopmentPlainLoopback: true, ConnectionQueueFrames: 1}, rooms, &integrationTickets{})
	if err != nil {
		t.Fatal(err)
	}
	s.ctx = ctx
	socket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	s.udp = socket
	readerDone := make(chan struct{})
	go func() { defer close(readerDone); s.readUDP(socket) }()
	defer func() {
		cancel()
		if err := socket.Close(); err != nil {
			t.Error(err)
		}
		<-readerDone
	}()
	makeConnection := func(name string, marker byte) *connection {
		t.Helper()
		id := make([]byte, 16)
		id[0] = marker
		key := make([]byte, 32)
		key[0] = marker
		c := &connection{server: s, native: room.Connection{ID: name, Channel: room.Action}, id: id, key: key, epoch: 1,
			ctx: ctx, done: make(chan struct{}), udpIncoming: make(chan udpRequest, 1)}
		s.mu.Lock()
		s.connections[hex.EncodeToString(id)] = c
		s.mu.Unlock()
		return c
	}
	slow, fast := makeConnection("slow", 1), makeConnection("fast", 2)
	workerDone := make(chan struct{}, 2)
	for _, c := range []*connection{slow, fast} {
		go func() { defer func() { workerDone <- struct{}{} }(); c.readUDPQueue() }()
	}
	defer func() { cancel(); close(slow.done); close(fast.done); <-workerDone; <-workerDone }()
	sender, err := net.DialUDP("udp", nil, socket.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sender.Close(); err != nil {
			t.Error(err)
		}
	}()
	send := func(c *connection, sequence uint64) {
		t.Helper()
		frame := EncodeFrame(room.Frame{Type: 68, Sequence: int32(sequence)})
		if _, err := sender.Write(encodeDatagram(c.id, c.epoch, sequence, frame, directionalKey(c.key, "client"))); err != nil {
			t.Fatal(err)
		}
	}
	send(slow, 1)
	select {
	case <-rooms.entered:
	case <-time.After(time.Second):
		t.Fatal("slow request was not dispatched")
	}
	send(slow, 2)
	send(fast, 1)
	select {
	case processed := <-rooms.processed:
		if processed.connection != "fast" || processed.sequence != 1 {
			t.Fatalf("same-connection order was not preserved: %+v", processed)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked room stalled another connection")
	}
	close(rooms.release)
	for sequence := int32(1); sequence <= 2; sequence++ {
		select {
		case processed := <-rooms.processed:
			if processed.connection != "slow" || processed.sequence != sequence {
				t.Fatalf("unexpected dispatch: %+v, want slow sequence%d", processed, sequence)
			}
		case <-time.After(time.Second):
			t.Fatal("queued request was lost")
		}
	}
}

func TestNativeTLSRequiresTrustedCertificateAndCorrectHostname(t *testing.T) {
	certificateServer := httptest.NewTLSServer(nil)
	certificate := certificateServer.TLS.Certificates[0]
	certificateServer.Close()
	key, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certPath, keyPath := filepath.Join(directory, "certificate.pem"), filepath.Join(directory, "key.pem")
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := room.New(room.Config{AdvertisedHost: "127.0.0.1", TCPPort: 9000}, integrationRules{})
	if err != nil {
		t.Fatal(err)
	}
	tickets := &integrationTickets{p: room.Principal{AccountID: "a", OwnerIndex: 91, SessionID: "s", Generation: 1}, used: map[string]bool{}}
	s, err := New(Config{TCPListen: "127.0.0.1:0", TLSCertFile: certPath, TLSKeyFile: keyPath}, m, tickets)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		closeCtx, cancelClose := context.WithTimeout(context.Background(), time.Second)
		defer cancelClose()
		if err := s.Close(closeCtx); err != nil {
			t.Error(err)
		}
		if err := m.Close(closeCtx); err != nil {
			t.Error(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-closeCtx.Done():
			t.Error("TLS listener did not close")
		}
	})
	var address string
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		s.mu.Lock()
		if s.listener != nil {
			address = s.listener.Addr().String()
		}
		s.mu.Unlock()
		if address != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if address == "" {
		t.Fatal("TLS listener did not start")
	}
	trusted := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	trusted.AddCert(parsed)
	for _, tc := range []struct {
		name, hostname string
		roots          *x509.CertPool
	}{{"unknown CA", "127.0.0.1", x509.NewCertPool()}, {"wrong hostname", "invalid.example", trusted}} {
		t.Run(tc.name, func(t *testing.T) {
			socket, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, &tls.Config{RootCAs: tc.roots, ServerName: tc.hostname, MinVersion: tls.VersionTLS12})
			if err == nil {
				if closeErr := socket.Close(); closeErr != nil {
					t.Error(closeErr)
				}
				t.Fatal("untrusted TLS peer connected")
			}
			var verification *tls.CertificateVerificationError
			if !errors.As(err, &verification) {
				t.Fatalf("connection failed without certificate verification: %v", err)
			}
		})
	}
	socket, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, &tls.Config{RootCAs: trusted, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := socket.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err = socket.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(AuthRequest{Ticket: "matching-ticket", Channel: "matching", InstanceID: "boot"})
	if err != nil {
		t.Fatal(err)
	}
	if err = writeAll(socket, append(append([]byte(AuthMagic), byte(len(request)), byte(len(request)>>8)), request...)); err != nil {
		t.Fatal(err)
	}
	var header [10]byte
	if _, err = io.ReadFull(socket, header[:]); err != nil {
		t.Fatal(err)
	}
	if string(header[:8]) != AckMagic {
		t.Fatal("TLS auth acknowledgment missing")
	}
	ack := make([]byte, binary.LittleEndian.Uint16(header[8:]))
	if _, err = io.ReadFull(socket, ack); err != nil {
		t.Fatal(err)
	}
	if err = writeAll(socket, EncodeFrame(room.Frame{Type: 11, OwnerIndex: 91, Sequence: 17})); err != nil {
		t.Fatal(err)
	}
	reply, err := ReadFrame(socket, 4096)
	if err != nil || reply.Type != 12 || reply.Sequence != 17 {
		t.Fatalf("native TLS exchange failed: %+v %v", reply, err)
	}
	rotatedKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := *parsed
	template.SerialNumber = big.NewInt(2)
	template.PublicKey = &rotatedKey.PublicKey
	rotated, err := x509.CreateCertificate(rand.Reader, &template, &template, &rotatedKey.PublicKey, rotatedKey)
	if err != nil {
		t.Fatal(err)
	}
	rotatedCertificate, err := x509.ParseCertificate(rotated)
	if err != nil {
		t.Fatal(err)
	}
	rotatedTrust := x509.NewCertPool()
	rotatedTrust.AddCert(rotatedCertificate)
	checkCertificate := func(roots *x509.CertPool, serial *big.Int) {
		t.Helper()
		peer, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, &tls.Config{RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12})
		if err != nil {
			t.Fatal(err)
		}
		if peer.ConnectionState().PeerCertificates[0].SerialNumber.Cmp(serial) != 0 {
			t.Error("listener retained the wrong certificate")
		}
		if err := peer.Close(); err != nil {
			t.Error(err)
		}
	}
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rotated}), 0600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(time.Second)
	if err = os.Chtimes(certPath, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	checkCertificate(trusted, parsed.SerialNumber)
	rotatedPKCS8, err := x509.MarshalPKCS8PrivateKey(rotatedKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rotatedPKCS8}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(keyPath, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	checkCertificate(rotatedTrust, template.SerialNumber)
}
