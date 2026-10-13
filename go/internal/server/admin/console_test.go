package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/gateway/auth"
)

type fakeIdentity struct{ next string }

func (f *fakeIdentity) Providers() []string { return []string{"discord"} }
func (f *fakeIdentity) BeginConsoleLogin(string, string) (auth.ConsoleLogin, error) {
	return auth.ConsoleLogin{ID: "tx", Secret: "secret", StartURL: "https://example.test/auth/discord/start", Expires: time.Now().Add(time.Minute)}, nil
}
func (f *fakeIdentity) FinishConsoleLogin(id, secret string) (string, bool, error) {
	return f.next, true, nil
}
func (f *fakeIdentity) AdministratorAccountID() (string, error) { return "owner", nil }

type browser struct {
	t      *testing.T
	server *httptest.Server
	client *http.Client
	csrf   string
}

func newConsole(t *testing.T) (*Console, *fakeIdentity, string) {
	t.Helper()
	root := t.TempDir()
	store, err := OpenStore(filepath.Join(root, "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	settings, _, err := inventory.DevelopmentSettingsDocument.Open(filepath.Join(root, "dev-tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	identity := &fakeIdentity{}
	spool := filepath.Join(root, "mail-grants-spool.json")
	console, err := New(Options{
		Store: store, Identity: identity, MailSpool: spool, Inventory: settings, Configuration: commonconfig.NewRegistry(),
		Players: func(context.Context) ([]Player, error) {
			return []Player{{AccountID: "owner", UID: "1", Active: true}, {AccountID: "friend", UID: "2", Active: true}, {AccountID: "other", UID: "3", Active: true}}, nil
		},
		Catalog: func() ([]gamedata.MailCatalogItem, error) {
			return []gamedata.MailCatalogItem{{ElementType: 4, Name: "gold", Category: "currency"}}, nil
		},
		Server: func() any { return map[string]string{} },
	})
	if err != nil {
		t.Fatal(err)
	}
	return console, identity, spool
}

func newBrowser(t *testing.T, server *httptest.Server) *browser {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{t: t, server: server, client: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) do(method, path string, body any, headers map[string]string) (int, map[string]any) {
	b.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	request, err := http.NewRequest(method, b.server.URL+path, reader)
	if err != nil {
		b.t.Fatal(err)
	}
	request.Header.Set("X-BD2-Console", "1")
	if b.csrf != "" {
		request.Header.Set("X-CSRF-Token", b.csrf)
	}
	for key, value := range headers {
		if value == "" {
			request.Header.Del(key)
		} else {
			request.Header.Set(key, value)
		}
	}
	response, err := b.client.Do(request)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var value map[string]any
	_ = json.NewDecoder(response.Body).Decode(&value)
	return response.StatusCode, value
}

func (b *browser) login(identity *fakeIdentity, account string) int {
	b.t.Helper()
	identity.next = account
	if status, _ := b.do(http.MethodPost, "/admin/api/login/start", map[string]string{"provider": "discord"}, nil); status != http.StatusOK {
		b.t.Fatalf("login start: %d", status)
	}
	status, _ := b.do(http.MethodPost, "/admin/api/login/poll", nil, nil)
	if status == http.StatusOK {
		_, session := b.do(http.MethodGet, "/admin/api/session", nil, nil)
		b.csrf, _ = session["csrf"].(string)
	}
	return status
}

func TestConsoleRolesFollowAdministratorAndOwnerGrants(t *testing.T) {
	console, identity, spool := newConsole(t)
	server := httptest.NewServer(console.Handler())
	defer server.Close()

	owner := newBrowser(t, server)
	if status := owner.login(identity, "owner"); status != http.StatusOK {
		t.Fatalf("administrator login: %d", status)
	}
	if status, _ := owner.do(http.MethodGet, "/admin/api/config/", nil, nil); status != http.StatusOK {
		t.Fatalf("owner configuration access: %d", status)
	}

	friend := newBrowser(t, server)
	if status := friend.login(identity, "friend"); status != http.StatusForbidden {
		t.Fatalf("ungranted account signed in: %d", status)
	}
	if status, _ := owner.do(http.MethodPut, "/admin/api/players/friend/member", map[string]bool{"member": true}, nil); status != http.StatusOK {
		t.Fatalf("grant member: %d", status)
	}
	if status := friend.login(identity, "friend"); status != http.StatusOK {
		t.Fatalf("granted member login: %d", status)
	}
	if status, _ := friend.do(http.MethodGet, "/admin/api/config/", nil, nil); status != http.StatusForbidden {
		t.Fatalf("member reached configuration: %d", status)
	}
	if _, value := friend.do(http.MethodGet, "/admin/api/players", nil, nil); len(value["players"].([]any)) != 1 {
		t.Fatalf("member saw other players: %v", value)
	}
	gift := map[string]any{"element_type": 4, "id": 0, "count": 5, "title": "t", "body": "b"}
	if status, _ := friend.do(http.MethodPost, "/admin/api/players/other/mail", gift, nil); status != http.StatusForbidden {
		t.Fatalf("member mailed another account: %d", status)
	}
	if status, _ := friend.do(http.MethodPost, "/admin/api/players/friend/mail", gift, nil); status != http.StatusCreated {
		t.Fatalf("member mail to self: %d", status)
	}
	raw, err := os.ReadFile(spool)
	if err != nil || !strings.Contains(string(raw), `"account_id": "friend"`) || strings.Contains(string(raw), `"account_id": "other"`) {
		t.Fatalf("spool recipients: %s, %v", raw, err)
	}

	if status, _ := owner.do(http.MethodPut, "/admin/api/players/friend/member", map[string]bool{"member": false}, nil); status != http.StatusOK {
		t.Fatalf("revoke member: %d", status)
	}
	if status, _ := friend.do(http.MethodGet, "/admin/api/players", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("revoked member session survived: %d", status)
	}
}

func TestConsoleStateChangesRequireSessionCSRFAndSameOrigin(t *testing.T) {
	console, identity, _ := newConsole(t)
	server := httptest.NewServer(console.Handler())
	defer server.Close()
	owner := newBrowser(t, server)
	owner.login(identity, "owner")
	change := map[string]bool{"unlimited": true}
	if status, _ := owner.do(http.MethodPut, "/admin/api/players/other/inventory", change, map[string]string{"X-CSRF-Token": ""}); status != http.StatusForbidden {
		t.Fatalf("missing CSRF token accepted: %d", status)
	}
	if status, _ := owner.do(http.MethodPut, "/admin/api/players/other/inventory", change, map[string]string{"Sec-Fetch-Site": "cross-site"}); status != http.StatusForbidden {
		t.Fatalf("cross-site request accepted: %d", status)
	}
	if status, value := owner.do(http.MethodPut, "/admin/api/players/other/inventory", change, nil); status != http.StatusOK || value["unlimited"] != true {
		t.Fatalf("inventory override: %d %v", status, value)
	}
}

func TestSignInLinkIsSingleUse(t *testing.T) {
	console, _, _ := newConsole(t)
	server := httptest.NewServer(console.Handler())
	defer server.Close()
	link, err := console.IssueSignInLink(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimPrefix(link, server.URL)
	operator := newBrowser(t, server)
	if status, _ := operator.do(http.MethodGet, path, nil, nil); status != http.StatusSeeOther {
		t.Fatalf("sign-in link: %d", status)
	}
	if _, session := operator.do(http.MethodGet, "/admin/api/session", nil, nil); session["role"] != string(RoleOwner) {
		t.Fatalf("link did not sign in the owner: %v", session)
	}
	if status, _ := newBrowser(t, server).do(http.MethodGet, path, nil, nil); status != http.StatusForbidden {
		t.Fatalf("sign-in link reused: %d", status)
	}
}
