// Package realtime authenticates native TCP and UDP before routing room commands.
package realtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"bd2server/internal/server/runtime/room"
)

type TicketConsumer interface {
	Consume(context.Context, string, string, string) (room.Principal, func(), error)
	Validate(context.Context, room.Principal) error
}
type Rooms interface {
	Handle(context.Context, room.Connection, room.Frame) ([]room.Delivery, error)
	Disconnect(context.Context, room.Connection) ([]room.Delivery, error)
}
type Config struct {
	TCPListen                string
	UDPListen                string
	TLSCertFile              string
	TLSKeyFile               string
	TrustedTLSProxyLoopback  bool
	DevelopmentPlainLoopback bool
	MaxConnections           int
	MaxConnectionsPerAccount int
	MaxFrameBytes            int
	ConnectionQueueFrames    int
	ConnectionQueueBytes     int64
	HandshakeTimeout         time.Duration
	IdleTimeout              time.Duration
	WriteTimeout             time.Duration
	ValidationInterval       time.Duration
}
type Server struct {
	config      Config
	rooms       Rooms
	tickets     TicketConsumer
	tls         *tls.Config
	mu          sync.Mutex
	connections map[string]*connection
	accounts    map[string]int
	listener    net.Listener
	udp         *net.UDPConn
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	slots       chan struct{}
	closed      bool
}
type connection struct {
	server      *Server
	native      room.Connection
	conn        net.Conn
	key         []byte
	id          []byte
	epoch       uint64
	send        chan room.Frame
	queuedBytes atomic.Int64
	closeOnce   sync.Once
	done        chan struct{}
	udpMu       sync.Mutex
	udpEndpoint *net.UDPAddr
	udpReplay   replayWindow
	udpSend     uint64
	udpIncoming chan udpRequest
	udpBytes    atomic.Int64
	ctx         context.Context
	cancel      context.CancelFunc
}

type udpRequest struct {
	frame    room.Frame
	endpoint *net.UDPAddr
}

func loopback(address string) bool {
	host, _, e := net.SplitHostPort(address)
	if e != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func New(config Config, rooms Rooms, tickets TicketConsumer) (*Server, error) {
	if rooms == nil || tickets == nil || config.TCPListen == "" {
		return nil, errors.New("realtime: rooms, tickets and TCP listener required")
	}
	if config.MaxConnections <= 0 {
		config.MaxConnections = 2048
	}
	if config.MaxConnectionsPerAccount <= 0 {
		config.MaxConnectionsPerAccount = 8
	}
	if config.MaxFrameBytes <= 0 {
		config.MaxFrameBytes = 1280000
	}
	if config.ConnectionQueueFrames <= 0 {
		config.ConnectionQueueFrames = 256
	}
	if config.ConnectionQueueBytes <= 0 {
		config.ConnectionQueueBytes = 4 << 20
	}
	if config.HandshakeTimeout <= 0 {
		config.HandshakeTimeout = 10 * time.Second
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = 30 * time.Second
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = 5 * time.Second
	}
	if config.ValidationInterval <= 0 {
		config.ValidationInterval = 10 * time.Second
	}
	s := &Server{config: config, rooms: rooms, tickets: tickets, connections: map[string]*connection{}, accounts: map[string]int{}, slots: make(chan struct{}, config.MaxConnections)}
	if config.TrustedTLSProxyLoopback || config.DevelopmentPlainLoopback {
		if !loopback(config.TCPListen) {
			return nil, errors.New("realtime: plaintext listener must bind a literal loopback address")
		}
	} else {
		cert, e := tls.LoadX509KeyPair(config.TLSCertFile, config.TLSKeyFile)
		if e != nil {
			return nil, fmt.Errorf("realtime TLS: %w", e)
		}
		var certificateMu sync.Mutex
		var certificateStamp, keyStamp time.Time
		currentCertificate := &cert
		s.tls = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			certificateMu.Lock()
			defer certificateMu.Unlock()
			certificateInfo, certificateErr := os.Stat(config.TLSCertFile)
			keyInfo, keyErr := os.Stat(config.TLSKeyFile)
			if certificateErr == nil && keyErr == nil && (!certificateInfo.ModTime().Equal(certificateStamp) || !keyInfo.ModTime().Equal(keyStamp)) {
				if next, err := tls.LoadX509KeyPair(config.TLSCertFile, config.TLSKeyFile); err == nil {
					currentCertificate = &next
					certificateStamp, keyStamp = certificateInfo.ModTime(), keyInfo.ModTime()
				}
			}
			return currentCertificate, nil
		}}
	}
	return s, nil
}

// Serve starts both listeners and blocks until cancellation or a listener failure.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	if s.closed || s.listener != nil {
		s.mu.Unlock()
		return errors.New("realtime: listener already started or closed")
	}
	listener, e := net.Listen("tcp", s.config.TCPListen)
	if e != nil {
		s.mu.Unlock()
		return e
	}
	if s.tls != nil {
		listener = tls.NewListener(listener, s.tls)
	}
	var udp *net.UDPConn
	if s.config.UDPListen != "" {
		addr, e := net.ResolveUDPAddr("udp", s.config.UDPListen)
		if e != nil {
			_ = listener.Close()
			s.mu.Unlock()
			return e
		}
		udp, e = net.ListenUDP("udp", addr)
		if e != nil {
			_ = listener.Close()
			s.mu.Unlock()
			return e
		}
	}
	s.listener = listener
	s.udp = udp
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()
	defer s.shutdown()
	if udp != nil {
		s.wg.Go(func() { s.readUDP(udp) })
	}
	go func() {
		<-s.ctx.Done()
		_ = listener.Close()
		if udp != nil {
			_ = udp.Close()
		}
	}()
	for {
		conn, e := listener.Accept()
		if e != nil {
			if s.ctx.Err() != nil {
				return nil
			}
			return e
		}
		select {
		case s.slots <- struct{}{}:
			s.wg.Go(func() { defer func() { <-s.slots }(); s.serveConnection(conn) })
		default:
			_ = conn.Close()
		}
	}
}
func (s *Server) shutdown() {
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
	if s.udp != nil {
		_ = s.udp.Close()
	}
	for _, c := range s.connections {
		c.close()
	}
	s.mu.Unlock()
}
func (s *Server) Close(ctx context.Context) error {
	s.shutdown()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *connection) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.cancel != nil {
			c.cancel()
		}
		_ = c.conn.Close()
	})
}
func (s *Server) serveConnection(socket net.Conn) {
	defer func() { _ = socket.Close() }()
	handshake, cancel := context.WithTimeout(s.ctx, s.config.HandshakeTimeout)
	defer cancel()
	_ = socket.SetDeadline(time.Now().Add(s.config.HandshakeTimeout))
	var header [10]byte
	if _, e := io.ReadFull(socket, header[:]); e != nil || string(header[:8]) != AuthMagic {
		return
	}
	length := binary.LittleEndian.Uint16(header[8:])
	if length == 0 || length > 2048 {
		return
	}
	body := make([]byte, length)
	if _, e := io.ReadFull(socket, body); e != nil {
		return
	}
	var auth AuthRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(&auth); e != nil || auth.Ticket == "" || len(auth.Ticket) > 1024 {
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		return
	}
	if auth.Channel != string(room.Matching) && auth.Channel != string(room.Action) && auth.Channel != string(room.Defense) && auth.Channel != string(room.Fishing) {
		return
	}
	principal, release, e := s.tickets.Consume(handshake, auth.Ticket, auth.Channel, auth.InstanceID)
	clear(body)
	if e != nil {
		slog.Warn("native realtime authentication failed", "channel", auth.Channel, "reason", e.Error())
		return
	}
	if release == nil {
		return
	}
	defer release()
	var id [16]byte
	var key [32]byte
	var epochBytes [8]byte
	if _, e = rand.Read(id[:]); e != nil {
		return
	}
	if _, e = rand.Read(key[:]); e != nil {
		return
	}
	if _, e = rand.Read(epochBytes[:]); e != nil {
		return
	}
	epoch := binary.LittleEndian.Uint64(epochBytes[:])
	if epoch == 0 {
		epoch = 1
	}
	c := &connection{server: s, native: room.Connection{ID: hex.EncodeToString(id[:]), Principal: principal, Channel: room.Kind(auth.Channel)}, conn: socket, id: append([]byte(nil), id[:]...), key: append([]byte(nil), key[:]...), epoch: epoch, send: make(chan room.Frame, s.config.ConnectionQueueFrames), done: make(chan struct{})}
	c.ctx, c.cancel = context.WithCancel(s.ctx)
	defer c.cancel()
	if c.native.Channel == room.Action {
		c.udpIncoming = make(chan udpRequest, s.config.ConnectionQueueFrames)
	}
	s.mu.Lock()
	if s.closed || s.accounts[principal.AccountID] >= s.config.MaxConnectionsPerAccount {
		s.mu.Unlock()
		return
	}
	for _, previous := range s.connections {
		if previous.native.Principal.AccountID == principal.AccountID && previous.native.Channel == c.native.Channel {
			previous.close()
		}
	}
	s.connections[c.native.ID] = c
	s.accounts[principal.AccountID]++
	s.mu.Unlock()
	defer func() {
		c.close()
		s.mu.Lock()
		delete(s.connections, c.native.ID)
		s.accounts[principal.AccountID]--
		if s.accounts[principal.AccountID] == 0 {
			delete(s.accounts, principal.AccountID)
		}
		s.mu.Unlock()
		c.udpMu.Lock()
		clear(c.key)
		c.udpMu.Unlock()
		d, _ := s.rooms.Disconnect(context.Background(), c.native)
		s.Deliver(d)
	}()
	ack := AuthAck{ConnectionID: c.native.ID, Epoch: epoch}
	if c.native.Channel == room.Action && s.udp != nil {
		ack.UDPKey = base64.StdEncoding.EncodeToString(c.key)
	}
	encoded, e := json.Marshal(ack)
	if e != nil {
		return
	}
	prefix := append([]byte(AckMagic), byte(len(encoded)), byte(len(encoded)>>8))
	if e = writeAll(socket, append(prefix, encoded...)); e != nil {
		return
	}
	_ = socket.SetDeadline(time.Time{})
	writerDone := make(chan struct{})
	go func() { defer close(writerDone); c.writeLoop() }()
	defer func() { c.close(); <-writerDone }()
	if c.udpIncoming != nil {
		udpDone := make(chan struct{})
		go func() { defer close(udpDone); c.readUDPQueue() }()
		defer func() { c.close(); <-udpDone }()
	}
	first := true
	for {
		_ = socket.SetReadDeadline(time.Now().Add(s.config.IdleTimeout))
		f, e := ReadFrame(socket, s.config.MaxFrameBytes)
		if e != nil {
			if !errors.Is(e, io.EOF) && c.ctx.Err() == nil {
				slog.Warn("native realtime connection closed", "channel", c.native.Channel, "account_id", c.native.Principal.AccountID, "reason", e.Error())
			}
			return
		}
		if first && f.Type != 11 {
			slog.Warn("native realtime initial packet rejected", "channel", c.native.Channel, "frame_type", f.Type)
			return
		}
		first = false
		d, e := s.rooms.Handle(c.ctx, c.native, f)
		if e != nil {
			slog.Warn("native realtime packet rejected", "channel", c.native.Channel, "account_id", c.native.Principal.AccountID, "frame_type", f.Type, "reason", e.Error())
			return
		}
		s.Deliver(d)
	}
}
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil {
			return e
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func (c *connection) writeLoop() {
	ticker := time.NewTicker(c.server.config.ValidationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if c.server.tickets.Validate(c.server.ctx, c.native.Principal) != nil {
				c.close()
				return
			}
		case <-c.done:
			return
		case f := <-c.send:
			c.queuedBytes.Add(-int64(20 + len(f.Payload)))
			if f.UDP && c.server.sendUDP(c, f) {
				continue
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(c.server.config.WriteTimeout))
			if e := writeAll(c.conn, EncodeFrame(f)); e != nil {
				c.close()
				return
			}
		}
	}
}
func (s *Server) Deliver(deliveries []room.Delivery) {
	for _, d := range deliveries {
		s.mu.Lock()
		c := s.connections[d.ConnectionID]
		s.mu.Unlock()
		if c == nil {
			continue
		}
		size := int64(20 + len(d.Frame.Payload))
		if c.queuedBytes.Add(size) > s.config.ConnectionQueueBytes {
			c.queuedBytes.Add(-size)
			c.close()
			continue
		}
		select {
		case <-c.done:
			c.queuedBytes.Add(-size)
		case c.send <- d.Frame:
		default:
			c.queuedBytes.Add(-size)
			c.close()
		}
	}
}
func (s *Server) readUDP(socket *net.UDPConn) {
	buffer := make([]byte, 65535)
	for {
		n, endpoint, e := socket.ReadFromUDP(buffer)
		if e != nil {
			return
		}
		if n < UDPOverhead || n > 60000+UDPOverhead {
			continue
		}
		packet := buffer[:n]
		if string(packet[:8]) != UDPMagic {
			continue
		}
		id := hex.EncodeToString(packet[8:24])
		s.mu.Lock()
		c := s.connections[id]
		s.mu.Unlock()
		if c == nil || c.native.Channel != room.Action {
			continue
		}
		c.udpMu.Lock()
		valid := binary.LittleEndian.Uint64(packet[24:32]) == c.epoch && verifyDatagram(packet, directionalKey(c.key, "client")) && c.udpReplay.accept(binary.LittleEndian.Uint64(packet[32:40]))
		c.udpMu.Unlock()
		if !valid {
			continue
		}
		payload := packet[44 : n-32]
		f, e := ReadFrame(bytes.NewReader(payload), 60000)
		if e != nil || len(payload) != 20+len(f.Payload) {
			continue
		}
		f.UDP = true
		if f.Type != 84 && f.Type != 68 && f.Type != 70 && f.Type != 72 && f.Type != 55 && f.Type != 25 && f.Type != 34 && f.Type != 27 && f.Type != 30 {
			continue
		}
		size := int64(20 + len(f.Payload))
		if c.udpBytes.Add(size) > s.config.ConnectionQueueBytes {
			c.udpBytes.Add(-size)
			continue
		}
		copyEndpoint := *endpoint
		select {
		case <-c.done:
			c.udpBytes.Add(-size)
		case c.udpIncoming <- udpRequest{frame: f, endpoint: &copyEndpoint}:
		default:
			c.udpBytes.Add(-size)
		}
	}
}

func (c *connection) readUDPQueue() {
	for {
		select {
		case <-c.done:
			return
		case request := <-c.udpIncoming:
			c.udpBytes.Add(-int64(20 + len(request.frame.Payload)))
			deliveries, err := c.server.rooms.Handle(c.ctx, c.native, request.frame)
			if err != nil {
				continue
			}
			c.udpMu.Lock()
			c.udpEndpoint = request.endpoint
			c.udpMu.Unlock()
			c.server.Deliver(deliveries)
		}
	}
}
func (s *Server) sendUDP(c *connection, f room.Frame) bool {
	c.udpMu.Lock()
	defer c.udpMu.Unlock()
	if c.udpEndpoint == nil || s.udp == nil {
		return false
	}
	c.udpSend++
	if c.udpSend == 0 {
		c.close()
		return true
	}
	packet := encodeDatagram(c.id, c.epoch, c.udpSend, EncodeFrame(f), directionalKey(c.key, "server"))
	_, e := s.udp.WriteToUDP(packet, c.udpEndpoint)
	return e == nil
}
