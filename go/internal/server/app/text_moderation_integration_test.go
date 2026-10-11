//go:build integration

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"bd2server/internal/server/design/gameconfig"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/gateway/session"
	"bd2server/internal/server/platform/textmoderation"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/player"
)

func TestTextModerationPreflightRejectsWholeBatchAndPreservesPreset(t *testing.T) {
	base := t.TempDir()
	config := gameconfig.DefaultTextModeration()
	config.RuleFiles = []string{moderationRulesFile(t, base, `{"rules":[{"text":"Blocked","match":"exact","scopes":["preset_name","master_title"]}]}`)}
	g := newModerationGameplay(t, config, base)
	metadata := wire.AppendVarint(wire.AppendString(nil, 2, "Allowed"), 3, 1)
	g.call("/PresetInfoChange", metadata, 0)
	beforePreset := g.call("/PresetInfo", nil, 0)
	beforeTitle := g.call("/MasterTitleInfo", nil, 0)
	before := g.snapshot()
	g.call("/PresetSave", wire.AppendBytes(nil, 2, wire.AppendVarint(wire.AppendString(nil, 1, "Blocked"), 2, 1)), 9001)
	g.call("/PresetInfoChange", wire.AppendVarint(wire.AppendString(nil, 2, "Blocked"), 3, 1), 9001)
	g.call("/MasterTitleInfoUpdate", wire.AppendString(nil, 2, "Blocked"), 9001)
	g.seq++
	request := registryCommand(g.account, "moderation-batch", "/UserNicknameChange", wire.AppendString(appScalar(1, g.seq), 2, "BatchAlias"))
	request.Requests = append(request.Requests, player.Request{Path: "/PresetInfoChange", Body: wire.AppendString(appScalar(1, g.seq+1), 2, "Blocked")})
	request.Digest = sha256.Sum256(append(request.Requests[0].Body, request.Requests[1].Body...))
	_, err := g.owner.Execute(context.Background(), request)
	var gameError *command.GameError
	if !errors.As(err, &gameError) || gameError.PacketCode != 278 || gameError.ErrorType != 9001 {
		t.Fatalf("batch rejection=%v, want PresetInfoChange moderation error", err)
	}
	g.assertState(before)
	if got := g.call("/PresetInfo", nil, 0); !bytes.Equal(got, beforePreset) {
		t.Fatal("rejected save, rename or batch changed persisted preset")
	}
	g.reopen()
	g.assertState(before)
	if got := g.call("/PresetInfo", nil, 0); !bytes.Equal(got, beforePreset) {
		t.Fatal("rejected preset mutation survived account reopen")
	}
	if got := g.call("/MasterTitleInfo", nil, 0); !bytes.Equal(got, beforeTitle) {
		t.Fatal("rejected master title mutation survived account reopen")
	}
}

func TestTextModerationProviderRunsOutsideTransactionAndCommittedReplaySkipsIt(t *testing.T) {
	var owner *playerInstance
	checks := make(chan error, 8)
	webhook := newModerationWebhook(t)
	webhook.check = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		tx, err := owner.repository.BeginCommand(ctx)
		if err == nil {
			err = tx.Rollback()
		}
		checks <- err
	}
	config := moderationHTTPConfig(webhook.server.URL, "deny", []string{"nickname", "monster_hunt_preset_name"})
	config.Provider.TimeoutMS = 1000
	g := newModerationGameplay(t, config, t.TempDir())
	owner = g.owner
	request := registryCommand(g.account, "moderation-replay", "/UserNicknameChange", wire.AppendString(appScalar(1, 101), 2, "AcceptedAlias"))
	first, err := g.owner.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-checks; err != nil {
		t.Fatalf("provider could not begin a transaction: player transaction was still open: %v", err)
	}
	before := g.snapshot()
	webhook.setMode("unavailable")
	g.reopen()
	owner = g.owner
	replay, err := g.owner.Execute(context.Background(), request)
	if err != nil || len(replay.Responses) != 1 || !bytes.Equal(replay.Responses[0].Body, first.Responses[0].Body) || replay.Responses[0].PacketCode != first.Responses[0].PacketCode {
		t.Fatalf("committed replay failed while provider unavailable: reply=%+v, error=%v", replay, err)
	}
	if got := webhook.requests(); len(got) != 1 {
		t.Fatalf("committed replay invoked provider again: %d calls", len(got))
	}
	g.assertState(before)
	g.call("/MonsterHuntPresetSave", wire.AppendBytes(nil, 2, nil), 0)
	if got := webhook.requests(); len(got) != 1 {
		t.Fatal("proto3 omitted monster hunt preset name was sent as empty text to provider")
	}
	rows := appRows(g.call("/MonsterHuntPresetInfo", nil, 0), 1)
	if len(rows) != 1 {
		t.Fatal("blank monster hunt preset was not stored")
	}
	name, _, err := wire.Bytes(rows[0], 1)
	if err != nil || len(name) != 0 {
		t.Fatal("blank monster hunt preset name did not persist")
	}
}

// These contracts are owner-defined server policy, including error types 9001/9002;
// transport and persistence use the real account actor, encrypted packets and SQLite.
func TestTextModerationDefaultAllowsFlechazo(t *testing.T) {
	g := newModerationGameplay(t, gameconfig.DefaultTextModeration(), t.TempDir())
	before := g.snapshot()
	g.edit("/UserNicknameChange", "Flechazo", 0)
	want := before
	want.name = "Flechazo"
	want.tickets--
	g.assertState(want)
	g.reopen()
	g.assertState(want)
}

func TestTextModerationOwnerRulesControlRealProfileEdits(t *testing.T) {
	base := t.TempDir()
	config := gameconfig.DefaultTextModeration()
	config.RuleFiles = []string{moderationRulesFile(t, base, `{"rules":[
		{"text":"lech","match":"word","scopes":["nickname"]},
		{"text":"BadAlias","match":"exact","scopes":["nickname"]},
		{"text":"secret","match":"substring","scopes":["nickname"]},
		{"text":"hazard","match":"substring","scopes":["nickname","greeting"],"exceptions":["SafeHazard"]},
		{"text":"SafeHazard","match":"exact","scopes":["nickname"]}
	]}`)}
	g := newModerationGameplay(t, config, base)
	for _, tc := range []struct {
		path, text string
		reject     bool
	}{
		{"/UserNicknameChange", "Flechazo", false},
		{"/UserNicknameChange", "lech", true},
		{"/UserNicknameChange", "bAdAlIaS", true},
		{"/UserNicknameChange", "xBadAlias", false},
		{"/UserNicknameChange", "secretNote", true},
		{"/UserGreetingChange", "secretNote", false},
		{"/UserGreetingChange", "lech visitors", false},
		{"/UserGreetingChange", "hazard", true},
		{"/UserGreetingChange", "SafeHazard", false},
		{"/UserGreetingChange", "SafeHazardExtra", true},
		{"/UserNicknameChange", "SafeHazard", true},
	} {
		t.Run(tc.path+"/"+tc.text, func(t *testing.T) {
			parent := g.t
			g.t = t
			defer func() { g.t = parent }()
			before := g.snapshot()
			if tc.reject {
				g.edit(tc.path, tc.text, 9001)
				g.assertState(before)
				g.reopen()
				g.assertState(before)
				return
			}
			g.edit(tc.path, tc.text, 0)
			want := before
			if tc.path == "/UserNicknameChange" {
				want.name = tc.text
				want.tickets--
			} else {
				want.greeting = tc.text
			}
			g.assertState(want)
		})
	}
	before := g.snapshot()
	g.reopen()
	g.assertState(before)
	g.edit("/UserNicknameChange", "NextAlias", 0)
	g.edit("/UserGreetingChange", "Welcome back", 0)
	want := before
	want.name, want.greeting = "NextAlias", "Welcome back"
	want.tickets--
	g.reopen()
	g.assertState(want)
}

func TestTextModerationRuleExceptionCannotBypassRemoteAndScopeGatesCalls(t *testing.T) {
	webhook := newModerationWebhook(t)
	base := t.TempDir()
	config := moderationHTTPConfig(webhook.server.URL, "deny", []string{"nickname"})
	config.RuleFiles = []string{moderationRulesFile(t, base, `{"rules":[{"text":"flech","match":"substring","scopes":["nickname"],"exceptions":["Flechazo"]}]}`)}
	g := newModerationGameplay(t, config, base)
	before := g.snapshot()
	g.edit("/UserGreetingChange", "flech visitors", 0)
	g.call("/ItemInfo", nil, 0)
	if got := webhook.requests(); len(got) != 0 {
		t.Fatalf("provider received requests outside configured nickname scope: %+v", got)
	}
	before.greeting = "flech visitors"
	webhook.setMode("reject")
	g.edit("/UserNicknameChange", "Flechazo", 9001)
	g.assertState(before)
	webhook.setMode("allow")
	g.edit("/UserNicknameChange", "Flechazo", 0)
	want := before
	want.name = "Flechazo"
	want.tickets--
	g.assertState(want)
	got := webhook.requests()
	if len(got) != 2 {
		t.Fatalf("remote should evaluate both attempts despite the local exception: %+v", got)
	}
	for _, request := range got {
		assertModerationWebhookRequest(t, request, g.account, "nickname", "Flechazo")
	}
	g.reopen()
	g.assertState(want)
}

func TestTextModerationHTTPDecisionsAndFailurePolicyPreserveAccount(t *testing.T) {
	for _, policy := range []string{"deny", "allow"} {
		t.Run(policy, func(t *testing.T) {
			webhook := newModerationWebhook(t)
			config := moderationHTTPConfig(webhook.server.URL, policy, []string{"nickname", "greeting"})
			g := newModerationGameplay(t, config, t.TempDir())
			g.edit("/UserNicknameChange", "InitialAlias", 0)
			g.edit("/UserGreetingChange", "Initial greeting", 0)
			for modeIndex, mode := range []string{"reject", "unavailable", "malformed", "timeout"} {
				for _, scope := range []string{"nickname", "greeting"} {
					t.Run(mode+"/"+scope, func(t *testing.T) {
						parent := g.t
						g.t = t
						defer func() { g.t = parent }()
						before := g.snapshot()
						path, candidate := "/UserNicknameChange", fmt.Sprintf("Candidate%d", modeIndex)
						if scope == "greeting" {
							path, candidate = "/UserGreetingChange", fmt.Sprintf("Candidate greeting %d", modeIndex)
						}
						webhook.setMode(mode)
						start := len(webhook.requests())
						wantError := 9002
						if mode == "reject" {
							wantError = 9001
						} else if policy == "allow" {
							wantError = 0
						}
						g.edit(path, candidate, wantError)
						want := before
						if wantError == 0 {
							if scope == "nickname" {
								want.name = candidate
								want.tickets--
							} else {
								want.greeting = candidate
							}
						}
						g.assertState(want)
						requests := webhook.requests()[start:]
						if len(requests) != 1 {
							t.Fatalf("profile edit produced %d provider requests", len(requests))
						}
						assertModerationWebhookRequest(t, requests[0], g.account, scope, candidate)
						if wantError != 0 {
							g.reopen()
							g.assertState(want)
							g.edit(path, candidate, wantError)
							g.assertState(want)
						}
						webhook.setMode("allow")
						valid := fmt.Sprintf("Recovery%d", modeIndex)
						if scope == "greeting" {
							valid = fmt.Sprintf("Recovery greeting %d", modeIndex)
						}
						g.edit(path, valid, 0)
						if scope == "nickname" {
							want.name = valid
							want.tickets--
						} else {
							want.greeting = valid
						}
						g.assertState(want)
						g.reopen()
						g.assertState(want)
					})
				}
			}
		})
	}
}

type moderationState struct {
	name, greeting string
	tickets        uint64
	wallet         assets.Currency
}

type moderationGameplay struct {
	t       *testing.T
	factory *PlayerFactory
	account string
	owner   *playerInstance
	runtime *player.Runtime
	server  *session.Server
	key     []byte
	cookie  string
	seq     uint64
	index   uint64
}

func newModerationGameplay(t *testing.T, config gameconfig.TextModerationConfig, base string) *moderationGameplay {
	t.Helper()
	factory, accounts := newIntegrationFactory(t)
	moderator, err := textmoderation.New(config, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := moderator.Close(); err != nil {
			t.Error(err)
		}
	})
	factory.options.textModerator = moderator
	g := &moderationGameplay{t: t, factory: factory, account: accounts[0]}
	identity, err := factory.profiles.GameIdentity(context.Background(), g.account)
	if err != nil {
		t.Fatal(err)
	}
	g.index = uint64(identity.OwnerIndex)
	owner := openNicknamePlayer(t, factory, g.account)
	seedNicknameTickets(t, owner, 20)
	closeNicknamePlayer(t, owner)
	g.open()
	t.Cleanup(func() { g.close() })
	return g
}

func (g *moderationGameplay) Acquire(_ context.Context, account string) (*player.Runtime, func(), error) {
	if account != g.account {
		return nil, nil, fmt.Errorf("unexpected account %q", account)
	}
	return g.runtime, func() {}, nil
}

func (g *moderationGameplay) open() {
	g.t.Helper()
	g.owner = openNicknamePlayer(g.t, g.factory, g.account)
	var err error
	g.runtime, err = player.New(g.account, g.owner, player.Limits{})
	if err != nil {
		closeNicknamePlayer(g.t, g.owner)
		g.t.Fatal(err)
	}
	g.server, err = session.NewServer(g, nicknameAuthenticator{g.account})
	if err != nil {
		g.t.Fatal(err)
	}
	fixed := []byte("abcdefghijkrstuv024680wxyzlmnopq")
	login, err := g.server.DispatchRaw(context.Background(), "/LoginUser", nicknameEncrypt(g.t, appScalar(1, 1), fixed), "")
	if err != nil {
		g.t.Fatal(err)
	}
	var envelope struct{ Data string }
	if err = json.Unmarshal(login.Body, &envelope); err != nil {
		g.t.Fatal(err)
	}
	users := appRows(nicknameDecrypt(g.t, envelope.Data, fixed), 1)
	if len(users) != 1 {
		g.t.Fatal("missing login user")
	}
	g.key = []byte(nicknameField(g.t, users[0], 3))
	if len(g.key) != 32 || login.Cookie == "" {
		g.t.Fatal("missing active session key/cookie")
	}
	g.cookie, g.seq = "s="+login.Cookie, 1
}

func (g *moderationGameplay) close() {
	g.t.Helper()
	if g.runtime != nil {
		if err := g.runtime.Close(context.Background()); err != nil {
			g.t.Error(err)
		}
		g.runtime = nil
	}
}

func (g *moderationGameplay) reopen() {
	g.t.Helper()
	g.close()
	g.open()
}

func (g *moderationGameplay) call(path string, fields []byte, wantError int) []byte {
	g.t.Helper()
	g.seq++
	request := append(appScalar(1, g.seq), fields...)
	reply, err := g.server.DispatchRaw(context.Background(), path, nicknameEncrypt(g.t, request, g.key), g.cookie)
	if err != nil {
		g.t.Fatalf("%s invalidated active session or failed dispatch: %v", path, err)
	}
	var envelope struct {
		PacketCode int
		ErrorType  int
		Data       string
	}
	if err = json.Unmarshal(reply.Body, &envelope); err != nil {
		g.t.Fatal(err)
	}
	if envelope.ErrorType != wantError {
		g.t.Fatalf("%s ErrorType=%d, want %d: %s", path, envelope.ErrorType, wantError, reply.Body)
	}
	if path == "/UserNicknameChange" && envelope.PacketCode != 74 || path == "/UserGreetingChange" && envelope.PacketCode != 219 {
		g.t.Fatalf("%s wrong packet code: %s", path, reply.Body)
	}
	return nicknameDecrypt(g.t, envelope.Data, g.key)
}

func (g *moderationGameplay) edit(path, text string, wantError int) {
	g.t.Helper()
	g.call(path, appMessage(2, []byte(text)), wantError)
}

func (g *moderationGameplay) snapshot() moderationState {
	g.t.Helper()
	body := g.call("/UserContentsInfo", appScalar(2, g.index), 0)
	tickets, wallet := nicknameAssets(g.t, g.owner)
	return moderationState{name: nicknameField(g.t, body, 2), greeting: nicknameField(g.t, body, 5), tickets: tickets, wallet: wallet}
}

func (g *moderationGameplay) assertState(want moderationState) {
	g.t.Helper()
	if got := g.snapshot(); got != want {
		g.t.Fatalf("account state=%+v, want %+v", got, want)
	}
}

func moderationRulesFile(t *testing.T, base, rules string) string {
	t.Helper()
	const name = "owner-rules.json"
	if err := os.WriteFile(filepath.Join(base, name), []byte(rules), 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func moderationHTTPConfig(endpoint, failurePolicy string, scopes []string) gameconfig.TextModerationConfig {
	config := gameconfig.DefaultTextModeration()
	config.Provider.Type = "http_json"
	config.Provider.Endpoint = endpoint
	config.Provider.Scopes = scopes
	config.Provider.TimeoutMS = 80
	config.Provider.FailurePolicy = failurePolicy
	return config
}

type moderationWebhookRequest struct {
	method, contentType string
	body                map[string]string
	decodeError         error
}

type moderationWebhook struct {
	server *httptest.Server
	mu     sync.Mutex
	mode   string
	seen   []moderationWebhookRequest
	check  func()
}

func newModerationWebhook(t *testing.T) *moderationWebhook {
	t.Helper()
	w := &moderationWebhook{mode: "allow"}
	w.server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body := map[string]string{}
		err := json.NewDecoder(request.Body).Decode(&body)
		w.mu.Lock()
		w.seen = append(w.seen, moderationWebhookRequest{request.Method, request.Header.Get("Content-Type"), body, err})
		mode := w.mode
		w.mu.Unlock()
		if w.check != nil {
			w.check()
		}
		switch mode {
		case "unavailable":
			response.WriteHeader(http.StatusServiceUnavailable)
		case "malformed":
			_, _ = response.Write([]byte(`{"decision":`))
		case "timeout":
			<-request.Context().Done()
		default:
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(map[string]string{"decision": mode})
		}
	}))
	t.Cleanup(w.server.Close)
	return w
}

func (w *moderationWebhook) setMode(mode string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.mode = mode
}

func (w *moderationWebhook) requests() []moderationWebhookRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.seen)
}

func assertModerationWebhookRequest(t *testing.T, request moderationWebhookRequest, account, scope, text string) {
	t.Helper()
	if request.decodeError != nil || request.method != http.MethodPost || request.contentType != "application/json" || len(request.body) != 3 || request.body["scope"] != scope || request.body["account_id"] != account || request.body["text"] != text {
		t.Fatalf("wrong provider request: %+v", request)
	}
}
