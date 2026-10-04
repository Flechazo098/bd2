// Package session owns authentication, encryption, and request routing for a
// local account. It deliberately has no capture/fixture dependency.
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"bd2server/internal/server/cryptox"
	"bd2server/internal/server/progress"
	"bd2server/internal/server/protocol"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/transport"
	"bd2server/internal/server/wire"
)

const (
	gameSessionTTL     = 24 * time.Hour
	maxGameSessions    = 1024
	maxCookieHeaderLen = 8 << 10
)

var errSessionRequired = errors.New("session login required")

type LoginService interface {
	Login(request, sessionKey []byte) ([]byte, error)
}

type LoginAuthenticator interface {
	AuthenticateLogin(request []byte) (accountID string, err error)
}

// Handler is implemented by domain services. ok=false means the endpoint is
// not owned by that service; unknown endpoints fail closed.
type Handler interface {
	Handle(path string, request []byte) (packetCode int, response []byte, ok bool, err error)
}

// SessionHandler receives the same opaque login identity for requests within
// a batch, allowing durable receipts without mutable global session state.
type SessionHandler interface {
	HandleSession(path string, request []byte, sessionID string) (packetCode int, response []byte, ok bool, err error)
}

// SessionAware handlers use a login-scoped opaque ID when protobuf request
// sequences participate in durable idempotency keys.
type SessionAware interface {
	BeginSession(id string)
}

// ResponseObserver runs inside the same transaction as the authoritative
// domain operation. It can derive progress and encode response notifications;
// any observer error rolls the entire request or batch back.
type ResponseObserver interface {
	BeforeDispatch(path string, request []byte) error
	AfterDispatch(path string, request, response []byte) ([]byte, error)
}

type gameSession struct {
	key       []byte
	accountID string
	id        string
	expiresAt time.Time
	lastUsed  time.Time
}

type Server struct {
	mu                 sync.Mutex
	sessions           map[[sha256.Size]byte]*gameSession
	latestSessionToken [sha256.Size]byte
	latestSessionSet   bool
	activeSessionID    string
	login              LoginService
	handlers           []Handler
	observers          []ResponseObserver
	progress           *progress.Store
	stateTx            stateio.TransactionalStore
	auth               LoginAuthenticator
	now                func() time.Time
	sessionTTL         time.Duration
	maxSessions        int
}

func (s *Server) AttachResponseObserver(observer ResponseObserver) error {
	if observer == nil {
		return errors.New("session response observer is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observers = append(s.observers, observer)
	return nil
}

func (s *Server) AttachLoginAuthenticator(authenticator LoginAuthenticator) error {
	if authenticator == nil {
		return errors.New("session login authenticator is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auth = authenticator
	return nil
}

// AttachStateStore wraps each authenticated request (the complete batch for
// BatchRequest) in one durable account database transaction.
func (s *Server) AttachStateStore(store stateio.TransactionalStore) error {
	if store == nil {
		return errors.New("session state store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stateTx = store
	return nil
}

func NewServer(login LoginService, handlers ...Handler) (*Server, error) {
	return NewServerWithProgress(login, progress.NewStore(), handlers...)
}

// NewServerWithProgress uses a caller-owned player progress store; the serve
// command supplies a file-backed store so changes survive server restarts.
func NewServerWithProgress(login LoginService, player *progress.Store, handlers ...Handler) (*Server, error) {
	if login == nil {
		return nil, errors.New("session login service is nil")
	}
	if player == nil {
		return nil, errors.New("session progress store is nil")
	}
	return &Server{
		sessions: make(map[[sha256.Size]byte]*gameSession), login: login,
		handlers: append([]Handler(nil), handlers...), progress: player,
		now: time.Now, sessionTTL: gameSessionTTL, maxSessions: maxGameSessions,
	}, nil
}

func (s *Server) DispatchRaw(path string, body []byte, cookie string) (transport.RawReply, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stateTx != nil {
		if err := s.stateTx.Check(); err != nil {
			return transport.RawReply{}, fmt.Errorf("account state unavailable: %w", err)
		}
	}
	if path == "/LoginUser" {
		now := s.now()
		s.pruneSessions(now)
		request, err := cryptox.DecryptBase64Payload(string(body), cryptox.Key())
		if err != nil {
			return transport.RawReply{}, fmt.Errorf("LoginUser decrypt: %w", err)
		}
		accountID := "local-owner"
		if s.auth != nil {
			accountID, err = s.auth.AuthenticateLogin(request)
			if err != nil {
				return transport.RawReply{}, fmt.Errorf("%w: %v", transport.ErrAccessCredentialInvalid, err)
			}
			if accountID == "" {
				return transport.RawReply{}, errors.New("LoginUser authentication returned an empty account ID")
			}
		}
		game, token, err := newGameSession(accountID, now, s.sessionTTL)
		if err != nil {
			return transport.RawReply{}, err
		}
		proto, err := s.login.Login(request, game.key)
		if err != nil {
			clear(game.key)
			return transport.RawReply{}, fmt.Errorf("LoginUser: %w", err)
		}
		encoded, err := protocol.Encode(3, proto, cryptox.Key(), now.UnixMilli())
		if err != nil {
			clear(game.key)
			return transport.RawReply{}, err
		}
		s.makeSessionRoom()
		tokenKey := sessionTokenKey(token)
		s.sessions[tokenKey] = game
		s.latestSessionToken = tokenKey
		s.latestSessionSet = true
		s.activate(game)
		return transport.RawReply{Body: encoded, Cookie: token}, nil
	}
	game, err := s.authorize(cookie)
	if err != nil {
		return transport.RawReply{}, err
	}
	s.activate(game)
	if path == "/BatchRequest" {
		return s.withStateTransaction(func() (transport.RawReply, error) {
			return s.handleBatch(body, game.key)
		})
	}
	request, err := cryptox.DecryptBase64Payload(string(body), game.key)
	if err != nil {
		return transport.RawReply{}, fmt.Errorf("%s decrypt: %w", path, err)
	}
	return s.withStateTransaction(func() (transport.RawReply, error) {
		code, response, notify, err := s.dispatchObserved(path, request)
		if err != nil {
			return transport.RawReply{}, err
		}
		encoded, err := protocol.EncodeWithNotify(code, response, game.key, s.now().UnixMilli(), notify)
		return transport.RawReply{Body: encoded}, err
	})
}

func newGameSession(accountID string, now time.Time, ttl time.Duration) (*gameSession, string, error) {
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, "", fmt.Errorf("create game session key: %w", err)
	}
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, "", fmt.Errorf("create game session token: %w", err)
	}
	idBytes := make([]byte, 12)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, "", fmt.Errorf("create login request identity: %w", err)
	}
	return &gameSession{
		key:       []byte(hex.EncodeToString(keyBytes)),
		accountID: accountID,
		id:        hex.EncodeToString(idBytes),
		expiresAt: now.Add(ttl),
		lastUsed:  now,
	}, hex.EncodeToString(tokenBytes) + "|1", nil
}

func sessionTokenKey(token string) [sha256.Size]byte { return sha256.Sum256([]byte(token)) }

func (s *Server) pruneSessions(now time.Time) {
	for token, game := range s.sessions {
		if !now.Before(game.expiresAt) {
			s.deleteSession(token, game)
		}
	}
}

func (s *Server) makeSessionRoom() {
	for len(s.sessions) >= s.maxSessions {
		var oldestToken [sha256.Size]byte
		var oldest *gameSession
		for token, game := range s.sessions {
			if oldest == nil || game.lastUsed.Before(oldest.lastUsed) {
				oldestToken, oldest = token, game
			}
		}
		if oldest == nil {
			return
		}
		s.deleteSession(oldestToken, oldest)
	}
}

func (s *Server) deleteSession(token [sha256.Size]byte, game *gameSession) {
	delete(s.sessions, token)
	for i := range game.key {
		game.key[i] = 0
	}
	game.accountID = ""
	game.id = ""
	if s.latestSessionSet && token == s.latestSessionToken {
		s.latestSessionSet = false
	}
}

func (s *Server) activate(game *gameSession) {
	s.activeSessionID = game.id
	for _, handler := range s.handlers {
		if aware, ok := handler.(SessionAware); ok {
			aware.BeginSession(game.id)
		}
	}
	for _, observer := range s.observers {
		if aware, ok := observer.(SessionAware); ok {
			aware.BeginSession(game.id)
		}
	}
}

func (s *Server) withStateTransaction(run func() (transport.RawReply, error)) (reply transport.RawReply, err error) {
	if s.stateTx == nil {
		return run()
	}
	operation, err := s.stateTx.BeginOperation()
	if err != nil {
		return transport.RawReply{}, fmt.Errorf("begin account transaction: %w", err)
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		rollbackErr := operation.Rollback()
		if recovered := recover(); recovered != nil {
			panic(recovered)
		}
		if rollbackErr != nil {
			err = errors.Join(err, rollbackErr)
		}
	}()
	reply, err = run()
	if err != nil {
		rollbackErr := operation.Rollback()
		finished = true
		if rollbackErr != nil {
			return transport.RawReply{}, errors.Join(err, rollbackErr)
		}
		return transport.RawReply{}, err
	}
	if err := operation.Commit(); err != nil {
		finished = true
		return transport.RawReply{}, fmt.Errorf("commit account transaction: %w", err)
	}
	finished = true
	return reply, nil
}

func (s *Server) handleBatch(body, key []byte) (transport.RawReply, error) {
	batchStarted := time.Now()
	requests, decoded, err := protocol.DecodeBatchRequest(body, key)
	if err != nil {
		return transport.RawReply{}, err
	}
	items := make([]protocol.BatchResponse, 0, len(requests))
	for i, request := range requests {
		itemStarted := time.Now()
		code, response, notify, err := s.dispatchObserved(request.Path, decoded[i])
		if err != nil {
			return transport.RawReply{}, fmt.Errorf("batch %s: %w", request.Path, err)
		}
		itemElapsed := time.Since(itemStarted)
		if formationTimingPath(request.Path) {
			slog.Info("formation batch item handled", "index", i, "path", request.Path, "duration_ms", float64(itemElapsed.Microseconds())/1000, "response_bytes", len(response))
		} else if itemElapsed >= 100*time.Millisecond {
			slog.Warn("slow batch item", "index", i, "path", request.Path, "duration_ms", float64(itemElapsed.Microseconds())/1000)
		}
		raw, err := protocol.EncodeWithNotify(code, response, key, time.Now().UnixMilli(), notify)
		if err != nil {
			return transport.RawReply{}, err
		}
		var envelope protocol.Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return transport.RawReply{}, err
		}
		items = append(items, protocol.BatchResponse{Path: request.Path, ResponseData: envelope})
	}
	encoded, err := json.Marshal(items)
	batchElapsed := time.Since(batchStarted)
	if batchElapsed >= time.Second {
		slog.Warn("slow batch request", "items", len(requests), "duration_ms", float64(batchElapsed.Microseconds())/1000, "responseBytes", len(encoded))
	}
	return transport.RawReply{Body: encoded}, err
}

func formationTimingPath(path string) bool {
	return strings.HasPrefix(path, "/Preset") ||
		strings.HasPrefix(path, "/Deck") ||
		strings.HasPrefix(path, "/FieldDeck") ||
		path == "/EquipBatchUse"
}

func (s *Server) dispatch(path string, request []byte) (int, []byte, error) {
	if _, found, err := wire.Varint(request, 1); err != nil || !found {
		return 0, nil, fmt.Errorf("%s has no request sequence", path)
	}
	switch path {
	case "/SaveUserPosition":
		if err := s.progress.SaveUserPosition(request); err != nil {
			return 0, nil, fmt.Errorf("%s: %w", path, err)
		}
		if saved, found := s.progress.Position(); found {
			slog.Info("field position saved", "pack", saved.PackID, "map", saved.Position.MapID)
		}
		return 7, nil, nil
	case "/TutorialClear":
		if err := s.progress.ClearTutorial(request); err != nil {
			return 0, nil, fmt.Errorf("%s: %w", path, err)
		}
		return 102, nil, nil
	}
	for _, handler := range s.handlers {
		var code int
		var response []byte
		var ok bool
		var err error
		if scoped, supports := handler.(SessionHandler); supports {
			if s.activeSessionID == "" {
				return 0, nil, errSessionRequired
			}
			code, response, ok, err = scoped.HandleSession(path, request, s.activeSessionID)
		} else {
			code, response, ok, err = handler.Handle(path, request)
		}
		if err != nil {
			return 0, nil, err
		}
		if ok {
			return code, response, nil
		}
	}
	return 0, nil, fmt.Errorf("%w: %s", transport.ErrNotImplemented, path)
}

func (s *Server) authorize(cookie string) (*gameSession, error) {
	token, err := parseSessionCookie(cookie)
	if err != nil {
		if errors.Is(err, errSessionRequired) {
			return nil, fmt.Errorf("%w: %v", transport.ErrGameSessionExpired, err)
		}
		return nil, err
	}
	now := s.now()
	s.pruneSessions(now)
	game, ok := s.sessions[sessionTokenKey(token)]
	if !ok {
		return nil, fmt.Errorf("%w: cookie no longer names a live session", transport.ErrGameSessionExpired)
	}
	game.lastUsed = now
	return game, nil
}

func parseSessionCookie(cookie string) (string, error) {
	if cookie == "" {
		return "", errSessionRequired
	}
	if len(cookie) > maxCookieHeaderLen {
		return "", errors.New("game session cookie header is too large")
	}
	var token string
	seen := false
	for _, value := range strings.Split(cookie, ";") {
		name, candidate, found := strings.Cut(strings.TrimSpace(value), "=")
		if !found || name != "s" {
			continue
		}
		if seen {
			return "", errors.New("duplicate game session cookie")
		}
		seen = true
		token = candidate
	}
	if !seen {
		return "", errSessionRequired
	}
	if len(token) != 50 || token[48:] != "|1" {
		return "", errors.New("invalid game session cookie")
	}
	for _, char := range token[:48] {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return "", errors.New("invalid game session cookie")
		}
	}
	return token, nil
}

// KeyForTest returns the most recently created session key when called without
// a token. Supplying a raw cookie value selects that client's isolated key.
func (s *Server) KeyForTest(token ...string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	selected := s.latestSessionToken
	if len(token) != 0 {
		selected = sessionTokenKey(token[0])
	} else if !s.latestSessionSet {
		return nil
	}
	game := s.sessions[selected]
	if game == nil {
		return nil
	}
	return append([]byte(nil), game.key...)
}

func (s *Server) ProgressForTest() *progress.Store { return s.progress }
