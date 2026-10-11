//go:build integration

package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bd2server/internal/server/gateway/session"
	"bd2server/internal/server/runtime/room"
)

func TestRealtimeTicketUsesCurrentNativeGameSessionAndOneUseBindings(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	registry := newPlayerRegistry(factory, time.Minute)
	t.Cleanup(func() {
		if err := registry.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	game, err := session.NewServer(registry, nicknameAuthenticator{accounts[0]})
	if err != nil {
		t.Fatal(err)
	}
	login := func() string {
		t.Helper()
		reply, err := game.DispatchRaw(context.Background(), "/LoginUser", nicknameEncrypt(t, appScalar(1, 1), []byte("abcdefghijkrstuv024680wxyzlmnopq")), "")
		if err != nil {
			t.Fatal(err)
		}
		return "s=" + reply.Cookie
	}
	cookie := login()
	tickets := &realtimeTickets{game: game, players: registry, bootID: "boot", ownerIndex: func(_ context.Context, account string) (int64, error) {
		if account != accounts[0] {
			return 0, errors.New("wrong account")
		}
		return 91, nil
	}}
	issue := func(cookie string, want int) string {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/realtime/ticket", strings.NewReader(`{"channel":"matching"}`))
		request.Header.Set("Cookie", cookie)
		reply := httptest.NewRecorder()
		tickets.ServeHTTP(reply, request)
		if reply.Code != want {
			t.Fatalf("ticket status=%d want%d: %s", reply.Code, want, reply.Body.String())
		}
		if want != http.StatusOK {
			return ""
		}
		var body struct {
			Ticket   string `json:"ticket"`
			Instance string `json:"instance_id"`
		}
		if err := json.Unmarshal(reply.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Ticket) != 43 || body.Instance != "boot" {
			t.Fatalf("invalid ticket response: %+v", body)
		}
		return body.Ticket
	}
	issue("", http.StatusUnauthorized)
	issue("s=invalid", http.StatusUnauthorized)
	principal, err := game.Principal(context.Background(), cookie)
	if err != nil {
		t.Fatal(err)
	}
	token := issue(cookie, http.StatusOK)
	p, release, err := tickets.Consume(context.Background(), token, "matching", "boot")
	if err != nil {
		t.Fatal(err)
	}
	if release == nil {
		t.Fatal("ticket omitted player lease")
	}
	release()
	if p.AccountID != principal.AccountID || p.SessionID != principal.SessionID || p.Generation != principal.Generation || p.OwnerIndex != 91 {
		t.Fatalf("ticket principal differs from native session: %+v vs %+v", p, principal)
	}
	if _, _, err = tickets.Consume(context.Background(), token, "matching", "boot"); !errors.Is(err, room.ErrUnauthorized) {
		t.Fatalf("ticket replay accepted: %v", err)
	}
	for _, tc := range []struct{ name, channel, instance string }{{"channel", "action", "boot"}, {"instance", "matching", "other-boot"}} {
		t.Run(tc.name, func(t *testing.T) {
			token := issue(cookie, http.StatusOK)
			if _, _, err := tickets.Consume(context.Background(), token, tc.channel, tc.instance); !errors.Is(err, room.ErrUnauthorized) {
				t.Fatalf("ticket binding accepted: %v", err)
			}
			if _, _, err := tickets.Consume(context.Background(), token, "matching", "boot"); !errors.Is(err, room.ErrUnauthorized) {
				t.Fatalf("failed ticket use was reusable: %v", err)
			}
		})
	}
	expired := issue(cookie, http.StatusOK)
	key := sha256.Sum256([]byte(expired))
	tickets.mu.Lock()
	entry := tickets.pending[key]
	entry.expires = time.Now().Add(-time.Second)
	tickets.pending[key] = entry
	tickets.mu.Unlock()
	if _, _, err = tickets.Consume(context.Background(), expired, "matching", "boot"); !errors.Is(err, room.ErrUnauthorized) {
		t.Fatalf("expired ticket accepted: %v", err)
	}
	stale := issue(cookie, http.StatusOK)
	newCookie := login()
	issue(cookie, http.StatusUnauthorized)
	if _, _, err = tickets.Consume(context.Background(), stale, "matching", "boot"); !errors.Is(err, room.ErrUnauthorized) {
		t.Fatalf("old game session retained ticket authority: %v", err)
	}
	current := issue(newCookie, http.StatusOK)
	p, release, err = tickets.Consume(context.Background(), current, "matching", "boot")
	if err != nil {
		t.Fatal(err)
	}
	if release == nil {
		t.Fatal("current ticket omitted lease")
	}
	release()
	if p.SessionID == principal.SessionID {
		t.Fatal("replacement login did not fence the old session")
	}
}
