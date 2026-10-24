package app

import (
	"bd2server/internal/common/util"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"bd2server/internal/server/gateway/session"
	"bd2server/internal/server/runtime/room"
)

type realtimeEndpoint struct {
	TCPHost          string `json:"tcp_host"`
	TCPPort          int    `json:"tcp_port"`
	UDPHost          string `json:"udp_host"`
	UDPPort          int    `json:"udp_port"`
	TLSServerName    string `json:"tls_server_name"`
	DevelopmentPlain bool   `json:"development_plain"`
}

type realtimeTicket struct {
	principal room.Principal
	channel   room.Kind
	instance  string
	expires   time.Time
}

type realtimeTickets struct {
	game       *session.Server
	players    session.RuntimeRouter
	rooms      *room.Manager
	ownerIndex func(context.Context, string) (int64, error)
	endpoint   realtimeEndpoint
	bootID     string
	mu         sync.Mutex
	pending    map[[sha256.Size]byte]realtimeTicket
}

func (t *realtimeTickets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, 2049))
	if err != nil || len(body) > 2048 {
		http.Error(w, "invalid ticket request", http.StatusBadRequest)
		return
	}
	var request struct {
		Channel  room.Kind `json:"channel"`
		Instance string    `json:"instance_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid ticket request", http.StatusBadRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid ticket request", http.StatusBadRequest)
		return
	}
	if request.Channel != room.Matching && request.Channel != room.Action && request.Channel != room.Defense && request.Channel != room.Fishing && request.Channel != room.Square {
		http.Error(w, "invalid channel", http.StatusBadRequest)
		return
	}
	p, err := t.game.Principal(r.Context(), r.Header.Get("Cookie"))
	if err != nil {
		http.Error(w, "game session required", http.StatusUnauthorized)
		return
	}
	owner, err := t.ownerIndex(r.Context(), p.AccountID)
	if err != nil || owner <= 0 {
		http.Error(w, "game identity unavailable", http.StatusServiceUnavailable)
		return
	}
	principal := room.Principal{AccountID: p.AccountID, OwnerIndex: owner, SessionID: p.SessionID, Generation: p.Generation}
	instance := t.bootID
	if request.Channel != room.Matching {
		view, err := t.rooms.View(r.Context(), principal, request.Channel)
		if err != nil {
			http.Error(w, "room membership required", http.StatusConflict)
			return
		}
		instance = view.InstanceID
	}
	if instance == "" || request.Instance != "" && request.Instance != instance {
		http.Error(w, "instance changed", http.StatusConflict)
		return
	}
	token, err := util.RandomBase64URL(32)
	if err != nil {
		http.Error(w, "ticket unavailable", http.StatusServiceUnavailable)
		return
	}
	key := sha256.Sum256([]byte(token))
	now := time.Now()
	expires := now.Add(30 * time.Second)
	t.mu.Lock()
	if t.pending == nil {
		t.pending = map[[sha256.Size]byte]realtimeTicket{}
	}
	for k, v := range t.pending {
		if !now.Before(v.expires) {
			delete(t.pending, k)
		}
	}
	count := 0
	for _, v := range t.pending {
		if v.principal.AccountID == p.AccountID {
			count++
		}
	}
	if count >= 8 || len(t.pending) >= 4096 {
		t.mu.Unlock()
		http.Error(w, "ticket capacity exhausted", http.StatusTooManyRequests)
		return
	}
	t.pending[key] = realtimeTicket{principal: principal, channel: request.Channel, instance: instance, expires: expires}
	t.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(struct {
		Ticket   string    `json:"ticket"`
		Expires  int64     `json:"expires_at"`
		Channel  room.Kind `json:"channel"`
		Instance string    `json:"instance_id"`
		realtimeEndpoint
	}{token, expires.UnixMilli(), request.Channel, instance, t.endpoint})
}

func (t *realtimeTickets) Consume(ctx context.Context, token, channel, instance string) (room.Principal, func(), error) {
	denied := func(format string, args ...any) (room.Principal, func(), error) {
		return room.Principal{}, nil, fmt.Errorf("%w: realtime ticket "+format, append([]any{room.ErrUnauthorized}, args...)...)
	}
	if len(token) != 43 {
		return denied("is %d characters, want 43", len(token))
	}
	key := sha256.Sum256([]byte(token))
	t.mu.Lock()
	ticket, found := t.pending[key]
	delete(t.pending, key)
	t.mu.Unlock()
	if !found || !time.Now().Before(ticket.expires) || string(ticket.channel) != channel || ticket.instance != instance {
		return denied("for %s room %q is unknown, expired or for another room (known=%t)", channel, instance, found)
	}
	owner, release, err := t.players.Acquire(ctx, ticket.principal.AccountID)
	if err != nil {
		return room.Principal{}, nil, err
	}
	if release == nil {
		return room.Principal{}, nil, errors.New("realtime player lease missing")
	}
	valid := false
	defer func() {
		if !valid {
			release()
		}
	}()
	if owner.Generation() != ticket.principal.Generation || t.Validate(ctx, ticket.principal) != nil {
		return denied("of owner %d belongs to an older session", ticket.principal.OwnerIndex)
	}
	if ticket.channel != room.Matching {
		view, err := t.rooms.View(ctx, ticket.principal, ticket.channel)
		if err != nil || view.InstanceID != instance {
			return denied("of owner %d names room %q, which the owner is not in: %v", ticket.principal.OwnerIndex, instance, err)
		}
	} else if instance != t.bootID {
		return denied("names matching boot %q, server boot is %q", instance, t.bootID)
	}
	valid = true
	return ticket.principal, release, nil
}

func (t *realtimeTickets) Validate(ctx context.Context, p room.Principal) error {
	return t.game.ValidatePrincipal(ctx, session.Principal{AccountID: p.AccountID, SessionID: p.SessionID, Generation: p.Generation})
}

func (t *realtimeTickets) RecordActivity(p room.Principal) {
	t.game.RecordActivity(session.Principal{AccountID: p.AccountID, SessionID: p.SessionID, Generation: p.Generation})
}
