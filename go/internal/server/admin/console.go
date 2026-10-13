// Package admin serves the server's built-in web console: configuration for
// the owner, and mail and inventory tools for the owner and granted members.
package admin

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/common/config/configapi"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/mail"
	"bd2server/internal/server/gateway/auth"
)

//go:embed web
var webFiles embed.FS

const (
	sessionCookie = "bd2_admin"
	loginCookie   = "bd2_admin_login"
	cookiePath    = "/admin/"
	linkTTL       = 24 * time.Hour
)

// Identity is the game's OAuth service, absent in local authentication mode.
type Identity interface {
	Providers() []string
	BeginConsoleLogin(provider, clientIP string) (auth.ConsoleLogin, error)
	FinishConsoleLogin(id, secret string) (accountID string, complete bool, err error)
	AdministratorAccountID() (string, error)
}

// Player is a game account visible to console tools.
type Player struct {
	AccountID string `json:"account_id"`
	UID       string `json:"uid"`
	Name      string `json:"name"`
	Active    bool   `json:"active"`
}

type CapacityLimits struct {
	Default uint64 `json:"default"`
	Maximum uint64 `json:"maximum"`
}

// InventoryLimits are the backpack capacities an unlimited override switches between.
type InventoryLimits struct {
	Items     CapacityLimits `json:"items"`
	Equipment CapacityLimits `json:"equipment"`
}

type Options struct {
	Store    *Store
	Identity Identity
	// Players lists every game account; it must include local-mode players.
	Players   func(context.Context) ([]Player, error)
	Catalog   func() ([]gamedata.MailCatalogItem, error)
	MailSpool string
	Inventory *commonconfig.File[inventory.DevelopmentSettings]
	Limits    InventoryLimits
	// Configuration is the owner's editable document registry.
	Configuration *commonconfig.Registry
	// Server describes the running process for the owner's overview.
	Server func() any
	// Secure marks cookies Secure; set when the public origin uses HTTPS.
	Secure bool
	Now    func() time.Time
}

type Console struct {
	options Options
	config  http.Handler

	mu      sync.Mutex
	pending map[string]*pendingLogin
	links   map[string]time.Time
}

type pendingLogin struct {
	login    auth.ConsoleLogin
	lastPoll time.Time
}

func New(options Options) (*Console, error) {
	if options.Store == nil || options.Players == nil || options.Catalog == nil || options.MailSpool == "" || options.Inventory == nil || options.Configuration == nil {
		return nil, errors.New("admin: incomplete console options")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	c := &Console{options: options, pending: map[string]*pendingLogin{}, links: map[string]time.Time{}}
	c.config = configapi.Handler(options.Configuration, configapi.Options{Changed: func(r *http.Request, view commonconfig.View) {
		slog.Info("configuration saved from console", "document", view.Name, "path", view.Path, "revision", view.Revision, "pending_restart", view.Pending)
	}})
	return c, nil
}

// IssueSignInLink creates a single-use owner sign-in link below base, the
// externally visible origin of this server. Whoever can read the server's
// output is its operator, so the link needs no other credential.
func (c *Console) IssueSignInLink(base string) (string, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.links[string(digest(token))] = c.options.Now().Add(linkTTL)
	c.mu.Unlock()
	return strings.TrimSuffix(base, "/") + "/admin/sign-in?token=" + url.QueryEscape(token), nil
}

func (c *Console) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFiles, "web")
	files := http.FileServerFS(static)
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
	mux.Handle("GET /admin/{$}", http.StripPrefix("/admin", files))
	mux.Handle("GET /admin/assets/", http.StripPrefix("/admin", files))
	mux.HandleFunc("GET /admin/sign-in", c.signInLink)
	mux.HandleFunc("GET /admin/api/session", c.sessionInfo)
	mux.HandleFunc("POST /admin/api/login/start", c.loginStart)
	mux.HandleFunc("POST /admin/api/login/poll", c.loginPoll)
	mux.HandleFunc("POST /admin/api/logout", c.logout)
	mux.HandleFunc("GET /admin/api/catalog", c.require(RoleMember, c.catalog))
	mux.HandleFunc("GET /admin/api/players", c.require(RoleMember, c.players))
	mux.HandleFunc("PUT /admin/api/players/{account}/member", c.require(RoleOwner, c.setMember))
	mux.HandleFunc("GET /admin/api/players/{account}/inventory", c.require(RoleMember, c.inventory))
	mux.HandleFunc("PUT /admin/api/players/{account}/inventory", c.require(RoleMember, c.setInventory))
	mux.HandleFunc("POST /admin/api/players/{account}/mail", c.require(RoleMember, c.sendMail))
	mux.HandleFunc("GET /admin/api/server", c.require(RoleOwner, func(w http.ResponseWriter, r *http.Request, _ session) {
		writeJSON(w, http.StatusOK, c.options.Server())
	}))
	mux.Handle("/admin/api/config/", c.require(RoleOwner, func(w http.ResponseWriter, r *http.Request, _ session) {
		http.StripPrefix("/admin/api/config", c.config).ServeHTTP(w, r)
	}))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type handler func(http.ResponseWriter, *http.Request, session)

// require authenticates the session, enforces the minimum role and, for
// state-changing methods, the session's CSRF token.
func (c *Console) require(minimum Role, next handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		current, ok := c.session(w, r)
		if !ok {
			return
		}
		if minimum == RoleOwner && current.Role != RoleOwner {
			fail(w, http.StatusForbidden, "forbidden", "owner permission required")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			supplied := r.Header.Get("X-CSRF-Token")
			if !sameOrigin(r) || subtle.ConstantTimeCompare([]byte(supplied), []byte(current.CSRF)) != 1 {
				fail(w, http.StatusForbidden, "csrf", "console page expired; reload it")
				return
			}
		}
		next(w, r, current)
	}
}

func (c *Console) session(w http.ResponseWriter, r *http.Request) (session, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		fail(w, http.StatusUnauthorized, "unauthorized", "sign in required")
		return session{}, false
	}
	current, ok, err := c.options.Store.Session(r.Context(), cookie.Value)
	if err != nil {
		fail(w, http.StatusInternalServerError, "unavailable", "session unavailable")
		return session{}, false
	}
	if !ok {
		c.clearCookie(w, sessionCookie)
		fail(w, http.StatusUnauthorized, "unauthorized", "sign in required")
		return session{}, false
	}
	return current, true
}

// sameOrigin rejects browser requests that a third-party page initiated.
func sameOrigin(r *http.Request) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	return site == "" || site == "same-origin"
}

func (c *Console) signInLink(w http.ResponseWriter, r *http.Request) {
	key := string(digest(r.URL.Query().Get("token")))
	c.mu.Lock()
	expires, ok := c.links[key]
	delete(c.links, key)
	c.mu.Unlock()
	if !ok || !c.options.Now().Before(expires) {
		http.Error(w, "This sign-in link is invalid, used or expired. Restart the server to print a new one.", http.StatusForbidden)
		return
	}
	if err := c.startSession(w, r.Context(), "", RoleOwner); err != nil {
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}
	slog.Info("console owner signed in with a server link")
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (c *Console) startSession(w http.ResponseWriter, ctx context.Context, accountID string, role Role) error {
	token, _, err := c.options.Store.CreateSession(ctx, accountID, role)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: cookiePath, MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, Secure: c.options.Secure, SameSite: http.SameSiteStrictMode})
	return nil
}

func (c *Console) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: cookiePath, MaxAge: -1, HttpOnly: true, Secure: c.options.Secure, SameSite: http.SameSiteStrictMode})
}

func (c *Console) sessionInfo(w http.ResponseWriter, r *http.Request) {
	info := map[string]any{"signed_in": false, "mode": "local", "providers": []string{}}
	if c.options.Identity != nil {
		info["mode"], info["providers"] = "oauth", c.options.Identity.Providers()
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		current, ok, err := c.options.Store.Session(r.Context(), cookie.Value)
		if err != nil {
			fail(w, http.StatusInternalServerError, "unavailable", "session unavailable")
			return
		}
		if ok {
			info["signed_in"], info["role"], info["csrf"] = true, current.Role, current.CSRF
			if current.AccountID != "" {
				if player, found, err := c.player(r.Context(), current.AccountID); err == nil && found {
					info["player"] = player
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, info)
}

func (c *Console) loginStart(w http.ResponseWriter, r *http.Request) {
	if c.options.Identity == nil {
		fail(w, http.StatusNotFound, "unsupported", "this server uses local authentication; sign in with the link printed by the server")
		return
	}
	if !sameOrigin(r) || r.Header.Get("X-BD2-Console") != "1" {
		fail(w, http.StatusForbidden, "csrf", "console page expired; reload it")
		return
	}
	var request struct {
		Provider string `json:"provider"`
	}
	if !decode(w, r, &request) {
		return
	}
	login, err := c.options.Identity.BeginConsoleLogin(request.Provider, clientIP(r))
	if errors.Is(err, auth.ErrRateLimited) {
		fail(w, http.StatusTooManyRequests, "rate_limited", "too many login attempts")
		return
	}
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid", "login could not be started")
		return
	}
	key, err := randomToken(24)
	if err != nil {
		fail(w, http.StatusInternalServerError, "unavailable", "login could not be started")
		return
	}
	c.mu.Lock()
	now := c.options.Now()
	for id, pending := range c.pending {
		if !now.Before(pending.login.Expires) {
			delete(c.pending, id)
		}
	}
	c.pending[key] = &pendingLogin{login: login}
	c.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: key, Path: cookiePath, MaxAge: int(login.Expires.Sub(now).Seconds()), HttpOnly: true, Secure: c.options.Secure, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]any{"start_url": login.StartURL, "poll_interval_ms": 2000})
}

func (c *Console) loginPoll(w http.ResponseWriter, r *http.Request) {
	if c.options.Identity == nil || !sameOrigin(r) || r.Header.Get("X-BD2-Console") != "1" {
		fail(w, http.StatusForbidden, "csrf", "console page expired; reload it")
		return
	}
	cookie, err := r.Cookie(loginCookie)
	if err != nil {
		fail(w, http.StatusGone, "expired", "login expired")
		return
	}
	c.mu.Lock()
	pending, ok := c.pending[cookie.Value]
	now := c.options.Now()
	throttled := ok && now.Sub(pending.lastPoll) < 1500*time.Millisecond
	if ok && !throttled {
		pending.lastPoll = now
	}
	c.mu.Unlock()
	if !ok || !now.Before(pending.login.Expires) {
		c.clearCookie(w, loginCookie)
		fail(w, http.StatusGone, "expired", "login expired")
		return
	}
	if throttled {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
		return
	}
	accountID, complete, err := c.options.Identity.FinishConsoleLogin(pending.login.ID, pending.login.Secret)
	if err == nil && !complete {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
		return
	}
	c.mu.Lock()
	delete(c.pending, cookie.Value)
	c.mu.Unlock()
	c.clearCookie(w, loginCookie)
	if err != nil || accountID == "" {
		fail(w, http.StatusForbidden, "rejected", "login was rejected")
		return
	}
	role, err := c.roleOf(r.Context(), accountID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "unavailable", "login unavailable")
		return
	}
	if role == "" {
		slog.Info("console login denied for account without access", "account", accountID)
		fail(w, http.StatusForbidden, "not_granted", "this game account has no console access")
		return
	}
	if err := c.startSession(w, r.Context(), accountID, role); err != nil {
		fail(w, http.StatusInternalServerError, "unavailable", "login unavailable")
		return
	}
	slog.Info("console signed in", "account", accountID, "role", role)
	writeJSON(w, http.StatusOK, map[string]string{"status": "complete"})
}

func (c *Console) roleOf(ctx context.Context, accountID string) (Role, error) {
	administrator, err := c.options.Identity.AdministratorAccountID()
	if err != nil {
		return "", err
	}
	if administrator != "" && administrator == accountID {
		return RoleOwner, nil
	}
	member, err := c.options.Store.IsMember(ctx, accountID)
	if err != nil || !member {
		return "", err
	}
	return RoleMember, nil
}

func (c *Console) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if !sameOrigin(r) {
			fail(w, http.StatusForbidden, "csrf", "console page expired; reload it")
			return
		}
		if err := c.options.Store.DeleteSession(r.Context(), cookie.Value); err != nil {
			fail(w, http.StatusInternalServerError, "unavailable", "sign out failed")
			return
		}
	}
	c.clearCookie(w, sessionCookie)
	w.WriteHeader(http.StatusNoContent)
}

func (c *Console) catalog(w http.ResponseWriter, _ *http.Request, _ session) {
	items, err := c.options.Catalog()
	if err != nil {
		slog.Error("console mail catalog unavailable", "error", err)
		fail(w, http.StatusInternalServerError, "unavailable", "mail catalog unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type playerView struct {
	Player
	Owner     bool `json:"owner"`
	Member    bool `json:"member"`
	Unlimited bool `json:"unlimited_inventory"`
}

func (c *Console) players(w http.ResponseWriter, r *http.Request, current session) {
	players, err := c.options.Players(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "unavailable", "player directory unavailable")
		return
	}
	members, err := c.options.Store.Members(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "unavailable", "player directory unavailable")
		return
	}
	administrator := ""
	if c.options.Identity != nil {
		if administrator, err = c.options.Identity.AdministratorAccountID(); err != nil {
			fail(w, http.StatusInternalServerError, "unavailable", "player directory unavailable")
			return
		}
	}
	settings, err := c.options.Inventory.Read()
	if err != nil {
		fail(w, http.StatusInternalServerError, "unavailable", "development settings unavailable")
		return
	}
	views := []playerView{}
	for _, player := range players {
		if current.Role != RoleOwner && player.AccountID != current.AccountID {
			continue
		}
		views = append(views, playerView{Player: player, Owner: player.AccountID == administrator, Member: members[player.AccountID],
			Unlimited: settings.Value.Accounts[player.AccountID].Inventory.Unlimited})
	}
	writeJSON(w, http.StatusOK, map[string]any{"players": views})
}

func (c *Console) player(ctx context.Context, accountID string) (Player, bool, error) {
	players, err := c.options.Players(ctx)
	if err != nil {
		return Player{}, false, err
	}
	for _, player := range players {
		if player.AccountID == accountID {
			return player, true, nil
		}
	}
	return Player{}, false, nil
}

// target resolves the {account} path value: owners may address any listed
// player, members only their own account.
func (c *Console) target(w http.ResponseWriter, r *http.Request, current session) (Player, bool) {
	account := r.PathValue("account")
	if current.Role != RoleOwner && account != current.AccountID {
		fail(w, http.StatusForbidden, "forbidden", "members may only manage their own account")
		return Player{}, false
	}
	player, found, err := c.player(r.Context(), account)
	if err != nil {
		fail(w, http.StatusInternalServerError, "unavailable", "player directory unavailable")
		return Player{}, false
	}
	if !found || !player.Active {
		fail(w, http.StatusNotFound, "not_found", "player not found or inactive")
		return Player{}, false
	}
	return player, true
}

func (c *Console) setMember(w http.ResponseWriter, r *http.Request, current session) {
	player, ok := c.target(w, r, current)
	if !ok {
		return
	}
	var request struct {
		Member *bool `json:"member"`
	}
	if !decode(w, r, &request) || request.Member == nil {
		if request.Member == nil {
			fail(w, http.StatusBadRequest, "invalid", "member flag required")
		}
		return
	}
	if err := c.options.Store.SetMember(r.Context(), player.AccountID, *request.Member); err != nil {
		fail(w, http.StatusInternalServerError, "unavailable", "member update failed")
		return
	}
	slog.Info("console member access changed", "account", player.AccountID, "uid", player.UID, "member", *request.Member)
	writeJSON(w, http.StatusOK, map[string]bool{"member": *request.Member})
}

func (c *Console) inventory(w http.ResponseWriter, r *http.Request, current session) {
	player, ok := c.target(w, r, current)
	if !ok {
		return
	}
	settings, err := c.options.Inventory.Read()
	if err != nil {
		fail(w, http.StatusInternalServerError, "unavailable", "development settings unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"unlimited": settings.Value.Accounts[player.AccountID].Inventory.Unlimited, "limits": c.options.Limits})
}

func (c *Console) setInventory(w http.ResponseWriter, r *http.Request, current session) {
	player, ok := c.target(w, r, current)
	if !ok {
		return
	}
	var request struct {
		Unlimited *bool `json:"unlimited"`
	}
	if !decode(w, r, &request) {
		return
	}
	if request.Unlimited == nil {
		fail(w, http.StatusBadRequest, "invalid", "unlimited flag required")
		return
	}
	_, err := c.options.Inventory.Update("", func(settings *inventory.DevelopmentSettings) error {
		if settings.Accounts == nil {
			settings.Accounts = map[string]inventory.DevelopmentAccount{}
		}
		if *request.Unlimited {
			settings.Accounts[player.AccountID] = inventory.DevelopmentAccount{Inventory: inventory.DevelopmentInventory{Unlimited: true}}
		} else {
			delete(settings.Accounts, player.AccountID)
		}
		return nil
	})
	if errors.Is(err, commonconfig.ErrReadOnly) {
		fail(w, http.StatusForbidden, "read_only", "development settings are read-only")
		return
	}
	if err != nil {
		slog.Error("console inventory override failed", "account", player.AccountID, "error", err)
		fail(w, http.StatusInternalServerError, "unavailable", "development settings unavailable")
		return
	}
	slog.Info("console inventory override changed", "account", player.AccountID, "uid", player.UID, "unlimited", *request.Unlimited)
	writeJSON(w, http.StatusOK, map[string]any{"unlimited": *request.Unlimited, "limits": c.options.Limits})
}

func (c *Console) sendMail(w http.ResponseWriter, r *http.Request, current session) {
	player, ok := c.target(w, r, current)
	if !ok {
		return
	}
	var request struct {
		ElementType uint64 `json:"element_type"`
		ID          uint64 `json:"id"`
		Count       uint64 `json:"count"`
		Title       string `json:"title"`
		Body        string `json:"body"`
	}
	if !decode(w, r, &request) {
		return
	}
	items, err := c.options.Catalog()
	if err != nil {
		fail(w, http.StatusInternalServerError, "unavailable", "mail catalog unavailable")
		return
	}
	known := false
	for _, item := range items {
		if item.ElementType == request.ElementType && item.ID == request.ID {
			known = true
			break
		}
	}
	title, body := strings.TrimSpace(request.Title), strings.TrimSpace(request.Body)
	switch {
	case !known:
		fail(w, http.StatusBadRequest, "invalid_item", "item is not in the direct mail catalog")
		return
	case request.Count == 0 || request.Count > math.MaxInt32:
		fail(w, http.StatusBadRequest, "invalid_count", "count must be between 1 and 2147483647")
		return
	case title == "" || utf8.RuneCountInString(request.Title) > 500 || body == "" || utf8.RuneCountInString(request.Body) > 5000:
		fail(w, http.StatusBadRequest, "invalid_text", "title (1-500) and body (1-5000) are required")
		return
	}
	identity, err := randomToken(18)
	if err != nil {
		fail(w, http.StatusInternalServerError, "unavailable", "mail unavailable")
		return
	}
	identity = "console-" + identity
	grant := mail.Grant{AccountID: player.AccountID, Identity: identity, Title: request.Title, Body: request.Body, SentAt: c.options.Now().UnixMilli(),
		Rewards: []mail.GrantReward{{Type: request.ElementType, ID: request.ID, Count: request.Count}}}
	if err := mail.AppendGrant(c.options.MailSpool, grant); err != nil {
		slog.Error("console mail grant failed", "account", player.AccountID, "error", err)
		fail(w, http.StatusInternalServerError, "unavailable", "mail could not be queued")
		return
	}
	slog.Info("console mail queued", "account", player.AccountID, "uid", player.UID, "identity", identity, "type", request.ElementType, "id", request.ID, "count", request.Count, "by", current.AccountID)
	writeJSON(w, http.StatusCreated, map[string]string{"identity": identity})
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.More() {
		fail(w, http.StatusBadRequest, "invalid", "invalid JSON request")
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func fail(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
