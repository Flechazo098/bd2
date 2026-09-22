// Package session owns authentication, encryption, and request routing for a
// local account. It deliberately has no capture/fixture dependency.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"bd2server/internal/cryptox"
	"bd2server/internal/progress"
	"bd2server/internal/protocol"
	"bd2server/internal/statetx"
	"bd2server/internal/transport"
	"bd2server/internal/wire"
)

type LoginService interface {
	Login(request, sessionKey []byte) ([]byte, error)
}

// Handler is implemented by domain services. ok=false means the endpoint is
// not owned by that service; unknown endpoints fail closed.
type Handler interface {
	Handle(path string, request []byte) (packetCode int, response []byte, ok bool, err error)
}

// SessionAware handlers use a login-scoped opaque ID when protobuf request
// sequences participate in durable idempotency keys.
type SessionAware interface {
	BeginSession(id string)
}

type StateCoordinator interface {
	Check() error
	BeginOperation() (statetx.RequestOperation, error)
}

type Server struct {
	mu       sync.Mutex
	key      []byte
	token    string
	loggedIn bool
	login    LoginService
	handlers []Handler
	progress *progress.Store
	stateTx  StateCoordinator
}

// AttachStateCoordinator wraps each authenticated request (the complete batch
// for BatchRequest) in the account write-ahead transaction. An uncertain
// operation fail-stops the dispatcher, including subsequent login attempts.
func (s *Server) AttachStateCoordinator(coordinator StateCoordinator) error {
	if coordinator == nil {
		return errors.New("session state coordinator is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stateTx = coordinator
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
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("create local session key: %w", err)
	}
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("create local session token: %w", err)
	}
	return &Server{
		key: []byte(hex.EncodeToString(key)), token: hex.EncodeToString(tokenBytes) + "|1",
		login: login, handlers: append([]Handler(nil), handlers...), progress: player,
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
		request, err := cryptox.DecryptBase64Payload(string(body), cryptox.Key())
		if err != nil {
			return transport.RawReply{}, fmt.Errorf("LoginUser decrypt: %w", err)
		}
		proto, err := s.login.Login(request, s.key)
		if err != nil {
			return transport.RawReply{}, fmt.Errorf("LoginUser: %w", err)
		}
		sessionBytes := make([]byte, 12)
		if _, err := rand.Read(sessionBytes); err != nil {
			return transport.RawReply{}, fmt.Errorf("create login request identity: %w", err)
		}
		sessionID := hex.EncodeToString(sessionBytes)
		for _, handler := range s.handlers {
			if aware, ok := handler.(SessionAware); ok {
				aware.BeginSession(sessionID)
			}
		}
		s.loggedIn = true
		encoded, err := protocol.Encode(3, proto, cryptox.Key(), time.Now().UnixMilli())
		return transport.RawReply{Body: encoded, Cookie: s.token}, err
	}
	if err := s.authorize(cookie); err != nil {
		return transport.RawReply{}, err
	}
	if path == "/BatchRequest" {
		return s.withStateTransaction(func() (transport.RawReply, error) {
			return s.handleBatch(body)
		})
	}
	request, err := cryptox.DecryptBase64Payload(string(body), s.key)
	if err != nil {
		return transport.RawReply{}, fmt.Errorf("%s decrypt: %w", path, err)
	}
	return s.withStateTransaction(func() (transport.RawReply, error) {
		code, response, err := s.dispatch(path, request)
		if err != nil {
			return transport.RawReply{}, err
		}
		encoded, err := protocol.Encode(code, response, s.key, time.Now().UnixMilli())
		return transport.RawReply{Body: encoded}, err
	})
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

func (s *Server) handleBatch(body []byte) (transport.RawReply, error) {
	requests, decoded, err := protocol.DecodeBatchRequest(body, s.key)
	if err != nil {
		return transport.RawReply{}, err
	}
	items := make([]protocol.BatchResponse, 0, len(requests))
	for i, request := range requests {
		code, response, err := s.dispatch(request.Path, decoded[i])
		if err != nil {
			return transport.RawReply{}, fmt.Errorf("batch %s: %w", request.Path, err)
		}
		raw, err := protocol.Encode(code, response, s.key, time.Now().UnixMilli())
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
	return transport.RawReply{Body: encoded}, err
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
		return 7, nil, nil
	case "/TutorialClear":
		if err := s.progress.ClearTutorial(request); err != nil {
			return 0, nil, fmt.Errorf("%s: %w", path, err)
		}
		return 102, nil, nil
	case "/QuestUpdate":
		questID, err := s.progress.UpdateQuest(request)
		if err != nil {
			return 0, nil, fmt.Errorf("%s: %w", path, err)
		}
		response := wire.AppendVarint(nil, 1, uint64(questID))
		return 19, wire.AppendBytes(response, 2, nil), nil
	}
	for _, handler := range s.handlers {
		code, response, ok, err := handler.Handle(path, request)
		if err != nil {
			return 0, nil, err
		}
		if ok {
			return code, response, nil
		}
	}
	return 0, nil, fmt.Errorf("%w: %s", transport.ErrNotImplemented, path)
}

func (s *Server) authorize(cookie string) error {
	if !s.loggedIn {
		return errors.New("session login required")
	}
	want := "s=" + s.token
	for _, value := range strings.Split(cookie, ";") {
		if strings.TrimSpace(value) == want {
			return nil
		}
	}
	return errors.New("invalid local session cookie")
}

func (s *Server) KeyForTest() []byte               { return append([]byte(nil), s.key...) }
func (s *Server) ProgressForTest() *progress.Store { return s.progress }
