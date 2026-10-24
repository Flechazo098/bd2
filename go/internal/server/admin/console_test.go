package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/server/design/gameconfig"
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
	game, _, err := gameconfig.Document.Open(filepath.Join(root, "config", "game.json"))
	if err != nil {
		t.Fatal(err)
	}
	identity := &fakeIdentity{}
	spool := filepath.Join(root, "mail-grants-spool.json")
	console, err := New(Options{
		Store: store, Identity: identity, MailSpool: spool, Settings: settings, Configuration: commonconfig.NewRegistry(game),
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

func TestConsoleTiersFollowOwnerAppointmentsAndPermissions(t *testing.T) {
	console, identity, spool := newConsole(t)
	server := httptest.NewServer(console.Handler())
	defer server.Close()

	owner := newBrowser(t, server)
	if status := owner.login(identity, "owner"); status != http.StatusOK {
		t.Fatalf("game administrator login: %d", status)
	}
	if status, _ := owner.do(http.MethodGet, "/admin/api/config/", nil, nil); status != http.StatusOK {
		t.Fatalf("owner configuration access: %d", status)
	}

	friend := newBrowser(t, server)
	if status := friend.login(identity, "friend"); status != http.StatusForbidden {
		t.Fatalf("ordinary player signed in: %d", status)
	}
	if status, value := owner.do(http.MethodPut, "/admin/api/players/friend/administrator", map[string]bool{"administrator": true}, nil); status != http.StatusOK ||
		len(value["permissions"].([]any)) != len(DefaultAdministratorPermissions) {
		t.Fatalf("appoint administrator with defaults: %d %v", status, value)
	}
	if status := friend.login(identity, "friend"); status != http.StatusOK {
		t.Fatalf("administrator login: %d", status)
	}

	gift := map[string]any{"element_type": 4, "id": 0, "count": 5, "title": "t", "body": "b"}
	if status, _ := friend.do(http.MethodPost, "/admin/api/players/friend/mail", gift, nil); status != http.StatusForbidden {
		t.Fatalf("default administrator mailed without an explicit grant: %d", status)
	}
	mailer := map[string]any{"administrator": true, "permissions": []string{PermissionMail}}
	if status, _ := owner.do(http.MethodPut, "/admin/api/players/friend/administrator", mailer, nil); status != http.StatusOK {
		t.Fatalf("grant mail: %d", status)
	}
	if status, _ := friend.do(http.MethodPost, "/admin/api/players/other/mail", gift, nil); status != http.StatusForbidden {
		t.Fatalf("administrator mailed another player: %d", status)
	}
	if status, _ := friend.do(http.MethodPost, "/admin/api/players/friend/mail", gift, nil); status != http.StatusCreated {
		t.Fatalf("administrator mailed own account: %d", status)
	}
	if status, _ := owner.do(http.MethodPost, "/admin/api/players/other/mail", gift, nil); status != http.StatusCreated {
		t.Fatalf("owner mailed another player: %d", status)
	}
	if raw, err := os.ReadFile(spool); err != nil || !strings.Contains(string(raw), `"account_id": "friend"`) || !strings.Contains(string(raw), `"account_id": "other"`) {
		t.Fatalf("spool recipients: %s, %v", raw, err)
	}
	if _, value := friend.do(http.MethodGet, "/admin/api/players", nil, nil); len(value["players"].([]any)) != 3 {
		t.Fatalf("default administrator player list: %v", value)
	}
	for _, denied := range []struct{ method, path string }{
		{http.MethodGet, "/admin/api/config/"},
		{http.MethodGet, "/admin/api/server"},
		{http.MethodGet, "/admin/api/permissions"},
		{http.MethodGet, "/admin/api/self/item-stacks"},
		{http.MethodPut, "/admin/api/self/item-stacks"},
		{http.MethodPut, "/admin/api/players/other/administrator"},
	} {
		if status, _ := friend.do(denied.method, denied.path, map[string]any{"unlimited": true, "administrator": true}, nil); status != http.StatusForbidden {
			t.Fatalf("default administrator reached %s %s: %d", denied.method, denied.path, status)
		}
	}

	grant := map[string]any{"administrator": true, "permissions": []string{"config.game.purchases.write"}}
	if status, value := owner.do(http.MethodPut, "/admin/api/players/friend/administrator", grant, nil); status != http.StatusOK ||
		!strings.Contains(fmt.Sprint(value["permissions"]), "config.game.purchases.read") {
		t.Fatalf("grant configuration section: %d %v", status, value)
	}
	if status, _ := owner.do(http.MethodPut, "/admin/api/players/friend/administrator", map[string]any{"administrator": true, "permissions": []string{"administrators.manage"}}, nil); status != http.StatusBadRequest {
		t.Fatalf("unknown permission accepted: %d", status)
	}
	if status, _ := owner.do(http.MethodPut, "/admin/api/players/owner/administrator", map[string]bool{"administrator": true}, nil); status != http.StatusBadRequest {
		t.Fatalf("owner appointed as administrator: %d", status)
	}
	if status, _ := friend.do(http.MethodPost, "/admin/api/players/friend/mail", gift, nil); status != http.StatusForbidden {
		t.Fatalf("replaced permissions kept mail: %d", status)
	}
	status, document := friend.do(http.MethodGet, "/admin/api/config/game", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("granted configuration section: %d", status)
	}
	sections := document["schema"].(map[string]any)["sections"].([]any)
	if len(sections) != 1 || sections[0].(map[string]any)["id"] != "purchases" {
		t.Fatalf("administrator saw ungranted sections: %v", sections)
	}

	stacker := map[string]any{"administrator": true, "permissions": []string{PermissionItemStacks}}
	if status, value := owner.do(http.MethodPut, "/admin/api/players/friend/administrator", stacker, nil); status != http.StatusOK ||
		!strings.Contains(fmt.Sprint(value["permissions"]), PermissionMail) {
		t.Fatalf("grant item stacks with its mail requirement: %d %v", status, value)
	}
	if status, value := friend.do(http.MethodPut, "/admin/api/self/item-stacks", map[string]bool{"unlimited": true}, nil); status != http.StatusOK || value["unlimited"] != true {
		t.Fatalf("administrator switched own item stacks: %d %v", status, value)
	}
	if status, value := owner.do(http.MethodGet, "/admin/api/self/item-stacks", nil, nil); status != http.StatusOK || value["unlimited"] != false {
		t.Fatalf("administrator's switch leaked to the owner: %d %v", status, value)
	}
	if raw, err := os.ReadFile(filepath.Join(filepath.Dir(spool), "dev-tools.json")); err != nil || !strings.Contains(string(raw), `"friend"`) || strings.Contains(string(raw), `"other"`) || strings.Contains(string(raw), `"owner"`) {
		t.Fatalf("item stack settings: %s, %v", raw, err)
	}
	if status, value := friend.do(http.MethodPut, "/admin/api/self/item-stacks", map[string]bool{"unlimited": false}, nil); status != http.StatusOK || value["unlimited"] != false {
		t.Fatalf("administrator switched own item stacks off: %d %v", status, value)
	}

	if status, _ := owner.do(http.MethodPut, "/admin/api/players/friend/administrator", map[string]bool{"administrator": false}, nil); status != http.StatusOK {
		t.Fatalf("remove administrator: %d", status)
	}
	if status, _ := friend.do(http.MethodGet, "/admin/api/players", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("removed administrator session survived: %d", status)
	}
}

func TestConsoleStateChangesRequireSessionCSRFAndSameOrigin(t *testing.T) {
	console, identity, _ := newConsole(t)
	server := httptest.NewServer(console.Handler())
	defer server.Close()
	owner := newBrowser(t, server)
	owner.login(identity, "owner")
	change := map[string]bool{"unlimited": true}
	if status, _ := owner.do(http.MethodPut, "/admin/api/self/item-stacks", change, map[string]string{"X-CSRF-Token": ""}); status != http.StatusForbidden {
		t.Fatalf("missing CSRF token accepted: %d", status)
	}
	if status, _ := owner.do(http.MethodPut, "/admin/api/self/item-stacks", change, map[string]string{"Sec-Fetch-Site": "cross-site"}); status != http.StatusForbidden {
		t.Fatalf("cross-site request accepted: %d", status)
	}
	if status, value := owner.do(http.MethodPut, "/admin/api/self/item-stacks", change, nil); status != http.StatusOK || value["unlimited"] != true {
		t.Fatalf("item stack override: %d %v", status, value)
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
