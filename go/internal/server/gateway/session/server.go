// Package session authenticates game sessions and routes commands to their account owner.
package session

import (
	"bd2server/internal/common/util"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/gateway/transport"
	"bd2server/internal/server/protocol"
	"bd2server/internal/server/protocol/cryptox"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/player"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	gameSessionTTL     = 24 * time.Hour
	maxGameSessions    = 1024
	maxCookieHeaderLen = 8 << 10
)

var errSessionRequired = errors.New("session login required")

type LoginAuthenticator interface{ AuthenticateLogin([]byte) (string, error) }
type RuntimeRouter interface {
	Acquire(context.Context, string) (*player.Runtime, func(), error)
}
type Handler interface {
	Handle(command.Context, string, []byte) (int, []byte, bool, error)
}
type ResponseObserver interface {
	BeforeDispatch(command.Context, string, []byte) error
	AfterDispatch(command.Context, string, []byte, []byte) ([]byte, error)
}
type LoginService interface {
	Login(command.Context, []byte, []byte) ([]byte, error)
}
type gameSession struct {
	key                 []byte
	accountID, id       string
	expiresAt, lastUsed time.Time
	generation          uint64
}
type Server struct {
	mu          sync.Mutex
	sessions    map[[sha256.Size]byte]*gameSession
	router      RuntimeRouter
	auth        LoginAuthenticator
	now         func() time.Time
	sessionTTL  time.Duration
	maxSessions int
	// Broadcast returns Notify fields appended to every game response.
	Broadcast func() []byte
	// LoggedIn receives an account's LoginUserRequest.language_type.
	LoggedIn func(accountID string, language uint64)
}

func NewServer(router RuntimeRouter, auth LoginAuthenticator) (*Server, error) {
	if router == nil {
		return nil, errors.New("session runtime router is nil")
	}
	return &Server{sessions: map[[sha256.Size]byte]*gameSession{}, router: router, auth: auth, now: time.Now, sessionTTL: gameSessionTTL, maxSessions: maxGameSessions}, nil
}
func (s *Server) DispatchRaw(ctx context.Context, path string, body []byte, cookie string) (reply transport.RawReply, err error) {
	started := time.Now()
	timing := requestTiming{}
	completed := false
	defer func() { timing.log(path, time.Since(started), len(reply.Body), err, completed) }()
	reply, err = s.dispatchRaw(ctx, path, body, cookie, &timing)
	completed = true
	return reply, err
}
func (s *Server) dispatchRaw(ctx context.Context, path string, body []byte, cookie string, timing *requestTiming) (transport.RawReply, error) {
	if err := ctx.Err(); err != nil {
		return transport.RawReply{}, err
	}
	if path == "/LoginUser" {
		return s.login(ctx, body, timing)
	}
	started := time.Now()
	game, err := s.authorize(cookie)
	timing.auth += time.Since(started)
	if err != nil {
		return transport.RawReply{}, err
	}
	defer clear(game.key)
	started = time.Now()
	var requests []player.Request
	if path == "/BatchRequest" {
		batch, decoded, err := protocol.DecodeBatchRequest(body, game.key)
		if err != nil {
			return transport.RawReply{}, err
		}
		for i, item := range batch {
			requests = append(requests, player.Request{Path: item.Path, Body: decoded[i]})
		}
	} else {
		plain, err := cryptox.DecryptBase64Payload(string(body), game.key)
		if err != nil {
			return transport.RawReply{}, fmt.Errorf("%s decrypt: %w", path, err)
		}
		requests = []player.Request{{Path: path, Body: plain}}
	}
	timing.decode += time.Since(started)
	timing.items = len(requests)
	identity, digest, err := requestIdentity(game, requests)
	if err != nil {
		return transport.RawReply{}, err
	}
	response, _, err := s.execute(ctx, player.Command{Identity: identity, Digest: digest, Requests: requests}, game.generation, timing)
	if err != nil {
		if path != "/BatchRequest" {
			if rejection, handled, encodeErr := s.gameErrorReply(game, err); handled {
				timing.rejected = true
				slog.WarnContext(ctx, "session game request rejected", "path", path, "error", rejectedRequestError(game.accountID, requests, err))
				return rejection, encodeErr
			}
		}
		return transport.RawReply{}, rejectedRequestError(game.accountID, requests, err)
	}
	if len(response.Responses) != len(requests) {
		return transport.RawReply{}, errors.New("player response count does not match command")
	}
	started = time.Now()
	defer func() { timing.encode += time.Since(started) }()
	if path != "/BatchRequest" {
		item := response.Responses[0]
		encoded, err := protocol.EncodeWithNotify(item.PacketCode, item.Body, game.key, s.now().UnixMilli(), s.notification(item.Notification))
		return transport.RawReply{Body: encoded}, err
	}
	items := make([]protocol.BatchResponse, 0, len(requests))
	for i, item := range response.Responses {
		envelope, err := protocol.EnvelopeWithNotify(item.PacketCode, item.Body, game.key, s.now().UnixMilli(), s.notification(item.Notification))
		if err != nil {
			return transport.RawReply{}, err
		}
		items = append(items, protocol.BatchResponse{Path: requests[i].Path, ResponseData: envelope})
	}
	encoded, err := json.Marshal(items)
	return transport.RawReply{Body: encoded}, err
}

// notification merges the server-wide Notify fields after the player's own;
// concatenated protobuf messages merge field by field.
func (s *Server) notification(player []byte) []byte {
	if s.Broadcast == nil {
		return player
	}
	shared := s.Broadcast()
	if len(shared) == 0 {
		return player
	}
	return append(append([]byte(nil), player...), shared...)
}
func (s *Server) execute(ctx context.Context, c player.Command, expectedGeneration uint64, timing *requestTiming) (player.Reply, uint64, error) {
	owner, release, err := s.router.Acquire(ctx, c.Identity.AccountID)
	if err != nil {
		return player.Reply{}, 0, err
	}
	if release == nil {
		return player.Reply{}, 0, errors.New("player runtime lease has no release")
	}
	defer release()
	generation := owner.Generation()
	if expectedGeneration != 0 && generation != expectedGeneration {
		return player.Reply{}, 0, transport.ErrGameSessionExpired
	}
	c.ExpectedGeneration = expectedGeneration
	future, err := owner.Submit(ctx, c)
	if err != nil {
		if errors.Is(err, player.ErrGenerationExpired) {
			return player.Reply{}, 0, transport.ErrGameSessionExpired
		}
		return player.Reply{}, 0, err
	}
	response, err := future.Wait(ctx)
	timing.queue += response.Timing.Queue
	timing.execute += response.Timing.Execute
	timing.observer += response.Timing.Observer
	timing.begin += response.Timing.Begin
	timing.commit += response.Timing.Commit
	timing.rollback += response.Timing.Rollback
	if expectedGeneration != 0 {
		var recovery interface{ RequiresRecovery() bool }
		if errors.Is(err, player.ErrGenerationExpired) || owner.Generation() != generation || (errors.As(err, &recovery) && recovery.RequiresRecovery()) {
			return player.Reply{}, 0, fmt.Errorf("%w: player state generation changed", transport.ErrGameSessionExpired)
		}
	}
	return response, response.Generation, err
}
func (s *Server) login(ctx context.Context, body []byte, timing *requestTiming) (transport.RawReply, error) {
	started := time.Now()
	request, err := cryptox.DecryptBase64Payload(string(body), cryptox.Key())
	timing.decode += time.Since(started)
	if err != nil {
		return transport.RawReply{}, fmt.Errorf("LoginUser decrypt: %w", err)
	}
	accountID := "local-owner"
	if s.auth != nil {
		started = time.Now()
		accountID, err = s.auth.AuthenticateLogin(request)
		timing.auth += time.Since(started)
		if err != nil {
			return transport.RawReply{}, fmt.Errorf("%w: %v", transport.ErrAccessCredentialInvalid, err)
		}
		if accountID == "" {
			return transport.RawReply{}, errors.New("LoginUser authentication returned an empty account ID")
		}
	}
	game, token, err := newGameSession(accountID, s.now(), s.sessionTTL)
	if err != nil {
		return transport.RawReply{}, err
	}
	requests := []player.Request{{Path: "/LoginUser", Body: request}}
	identity, digest, err := requestIdentity(game, requests)
	if err != nil {
		clear(game.key)
		return transport.RawReply{}, err
	}
	response, generation, err := s.execute(ctx, player.Command{Identity: identity, Digest: digest, Requests: requests, LoginSessionKey: game.key}, 0, timing)
	if err != nil {
		clear(game.key)
		return transport.RawReply{}, err
	}
	if len(response.Responses) != 1 {
		clear(game.key)
		return transport.RawReply{}, errors.New("login player response count invalid")
	}
	started = time.Now()
	encoded, err := protocol.Encode(3, response.Responses[0].Body, cryptox.Key(), s.now().UnixMilli())
	timing.encode += time.Since(started)
	if err != nil {
		clear(game.key)
		return transport.RawReply{}, err
	}
	s.mu.Lock()
	s.pruneSessions(s.now())
	for existingToken, existing := range s.sessions {
		if existing.accountID == accountID {
			s.deleteSession(existingToken, existing)
		}
	}
	s.makeSessionRoom()
	game.generation = generation
	s.sessions[sessionTokenKey(token)] = game
	s.mu.Unlock()
	if s.LoggedIn != nil {
		language, _, _ := wire.Varint(request, 7)
		s.LoggedIn(accountID, language)
	}
	return transport.RawReply{Body: encoded, Cookie: token}, nil
}
func requestIdentity(game *gameSession, requests []player.Request) (command.Identity, [32]byte, error) {
	sequences := sha256.New()
	content := sha256.New()
	var size [8]byte
	for _, req := range requests {
		seq, present, err := wire.Varint(req.Body, 1)
		if err != nil || !present || seq == 0 {
			return command.Identity{}, [32]byte{}, fmt.Errorf("%s has no request sequence", req.Path)
		}
		binary.BigEndian.PutUint64(size[:], uint64(len(req.Path)))
		_, _ = sequences.Write(size[:])
		_, _ = sequences.Write([]byte(req.Path))
		binary.BigEndian.PutUint64(size[:], seq)
		_, _ = sequences.Write(size[:])
		binary.BigEndian.PutUint64(size[:], uint64(len(req.Path)))
		_, _ = content.Write(size[:])
		_, _ = content.Write([]byte(req.Path))
		binary.BigEndian.PutUint64(size[:], uint64(len(req.Body)))
		_, _ = content.Write(size[:])
		_, _ = content.Write(req.Body)
	}
	var digest [32]byte
	copy(digest[:], content.Sum(nil))
	return command.Identity{AccountID: game.accountID, SessionID: game.id, RequestID: hex.EncodeToString(sequences.Sum(nil))}, digest, nil
}
func (s *Server) authorize(cookie string) (*gameSession, error) {
	token, err := parseSessionCookie(cookie)
	if err != nil {
		if errors.Is(err, errSessionRequired) {
			return nil, fmt.Errorf("%w: %v", transport.ErrGameSessionExpired, err)
		}
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneSessions(now)
	game, ok := s.sessions[sessionTokenKey(token)]
	if !ok {
		return nil, fmt.Errorf("%w: cookie no longer names a live session", transport.ErrGameSessionExpired)
	}
	game.lastUsed = now
	return &gameSession{key: append([]byte(nil), game.key...), accountID: game.accountID, id: game.id, expiresAt: game.expiresAt, lastUsed: game.lastUsed, generation: game.generation}, nil
}
func newGameSession(accountID string, now time.Time, ttl time.Duration) (*gameSession, string, error) {
	key, err := util.RandomHex(16)
	if err != nil {
		return nil, "", fmt.Errorf("create game session key: %w", err)
	}
	token, err := util.RandomHex(24)
	if err != nil {
		return nil, "", fmt.Errorf("create game session token: %w", err)
	}
	id, err := util.RandomHex(12)
	if err != nil {
		return nil, "", fmt.Errorf("create login request identity: %w", err)
	}
	return &gameSession{
		key:       []byte(key),
		accountID: accountID,
		id:        id,
		expiresAt: now.Add(ttl),
		lastUsed:  now,
	}, token + "|1", nil
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
	for value := range strings.SplitSeq(cookie, ";") {
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
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') { //nolint:staticcheck // QF1001
			return "", errors.New("invalid game session cookie")
		}
	}
	return token, nil
}
