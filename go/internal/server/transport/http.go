// Package transport owns the HTTP envelope and dispatches plaintext protobufs.
package transport

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"bd2server/internal/server/authconfig"
	"bd2server/internal/server/bootstrap"
	"bd2server/internal/server/resourcepolicy"
)

type Envelope struct {
	PacketCode    int    `json:"packetCode"`
	ErrorType     int    `json:"errorType"`
	ErrorMessage  string `json:"errorMessage"`
	Length        int    `json:"length"`
	Data          string `json:"data"`
	ServerNowTime int64  `json:"serverNowTime"`
	Notify        string `json:"notify,omitempty"`
	IP            string `json:"ip,omitempty"`
}

// Reply is a decoded protobuf message; encryption belongs to the session layer.
type Reply struct {
	PacketCode int
	Data       []byte
	Notify     string
	Cookie     string
}

type Dispatcher interface {
	Dispatch(path string, request []byte) (Reply, error)
}

type RawReply struct {
	Body        []byte
	ContentType string
	Cookie      string
}

// RawDispatcher handles encrypted/session packets whose outer representation
// is not always an Envelope (BatchRequest returns a JSON array).
type RawDispatcher interface {
	DispatchRaw(path string, wireBody []byte, cookie string) (RawReply, error)
}

type Bootstrap struct {
	Config bootstrap.Config
	Now    func() time.Time
}

var ErrNotImplemented = errors.New("packet not implemented")

func (b Bootstrap) Dispatch(path string, request []byte) (Reply, error) {
	switch path {
	case "/MaintenanceInfo":
		data, err := bootstrap.Maintenance(b.Config.Version, b.Config.BundleVer, request)
		return Reply{Data: data}, err
	case "/ServerInfo":
		return Reply{Data: bootstrap.ServerInfo(b.Config)}, nil
	case "/ServerNowTime":
		return Reply{Data: bootstrap.ServerNowTime(b.now())}, nil
	case "/NoticeInfo":
		return Reply{Data: bootstrap.NoticeInfo()}, nil
	default:
		return Reply{}, ErrNotImplemented
	}
}

func (b Bootstrap) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

type HTTP struct {
	Dispatcher            Dispatcher
	Raw                   RawDispatcher
	Logger                *slog.Logger
	Now                   func() time.Time
	Authentication        authconfig.Config
	AuthenticationHandler http.Handler
	ResourcePolicy        resourcepolicy.Public
}

func (h HTTP) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/StateCheckInfoJson", h.stateCheck)
	mux.HandleFunc("/game/StateCheckInfoJson", h.stateCheck)
	mux.HandleFunc("/auth/config", h.authenticationConfig)
	mux.HandleFunc("/client/resources", h.clientResources)
	if h.AuthenticationHandler != nil {
		mux.Handle("/auth/", h.AuthenticationHandler)
	}
	mux.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/game/", h.game)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

func (h HTTP) clientResources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "PUT required", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<10+1))
	if err != nil || len(body) > 16<<10 {
		http.Error(w, "resource selection too large", http.StatusRequestEntityTooLarge)
		return
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var request struct {
		CDNMode string `json:"cdn_mode"`
	}
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid resource selection", http.StatusBadRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid resource selection", http.StatusBadRequest)
		return
	}
	if request.CDNMode != resourcepolicy.ModeServer {
		http.Error(w, "cdn_mode must be server", http.StatusBadRequest)
		return
	}
	if h.ResourcePolicy.Mode == "" || h.ResourcePolicy.ServerDataURL == "" ||
		h.ResourcePolicy.GameDataURL == "" || h.ResourcePolicy.BundleVersion == "" ||
		h.ResourcePolicy.GameDataVersion == "" {
		http.Error(w, "resource policy unavailable", http.StatusServiceUnavailable)
		return
	}
	if request.CDNMode != h.ResourcePolicy.Mode {
		http.Error(w, "requested CDN mode is not enabled by this server", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(h.ResourcePolicy)
}

func (h HTTP) authenticationConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	if err := h.Authentication.Validate(); err != nil {
		http.Error(w, "authentication policy unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(h.Authentication.Public())
}

func (h HTTP) stateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"country": "CN", "errorType": 0, "errorMessage": ""})
}

func (h HTTP) game(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if r.Method != http.MethodPut {
		http.Error(w, "PUT required", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/game")
	if path == "" || path == "/" || strings.Contains(path[1:], "/") {
		http.NotFound(w, r)
		return
	}
	if h.Dispatcher == nil && h.Raw == nil {
		http.Error(w, "no dispatcher", http.StatusServiceUnavailable)
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20+1))
	if err != nil || len(body) > 8<<20 {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	bootstrapPacket := path == "/MaintenanceInfo" || path == "/ServerInfo" || path == "/ServerNowTime" || path == "/NoticeInfo"
	if !bootstrapPacket {
		if h.Raw == nil {
			h.logger().Info("packet needs session handler", "path", path)
			http.Error(w, "authenticated packet not yet implemented", http.StatusNotImplemented)
			return
		}
		reply, err := h.Raw.DispatchRaw(path, body, r.Header.Get("Cookie"))
		if err != nil {
			h.logger().Warn("session packet rejected", "path", path, "duration_ms", elapsedMilliseconds(started), "error", err)
			http.Error(w, "session packet rejected", http.StatusBadRequest)
			return
		}
		if reply.Cookie != "" {
			http.SetCookie(w, &http.Cookie{
				Name: "s", Value: reply.Cookie, Path: "/game/", HttpOnly: true,
				Secure: h.secureCookies(), SameSite: http.SameSiteLaxMode,
			})
		}
		if reply.ContentType == "" {
			reply.ContentType = "application/json; charset=utf-8"
		}
		w.Header().Set("Content-Type", reply.ContentType)
		h.logger().Info("session packet handled", "path", path, "duration_ms", elapsedMilliseconds(started), "responseBytes", len(reply.Body))
		_, _ = w.Write(reply.Body)
		return
	}
	if h.Dispatcher == nil {
		http.Error(w, "no bootstrap dispatcher", http.StatusServiceUnavailable)
		return
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body)))
	if err != nil {
		http.Error(w, "invalid base64 request", http.StatusBadRequest)
		return
	}
	reply, err := h.Dispatcher.Dispatch(path, decoded)
	if err != nil {
		h.logger().Error("bootstrap packet failed", "path", path, "error", err)
		if errors.Is(err, ErrNotImplemented) {
			http.Error(w, "packet not implemented", http.StatusNotImplemented)
		} else {
			http.Error(w, "bad packet", http.StatusBadRequest)
		}
		return
	}
	if reply.Cookie != "" {
		http.SetCookie(w, &http.Cookie{
			Name: "s", Value: reply.Cookie, Path: "/game/", HttpOnly: true,
			Secure: h.secureCookies(), SameSite: http.SameSiteLaxMode,
		})
	}
	answer := Envelope{PacketCode: reply.PacketCode, Data: base64.StdEncoding.EncodeToString(reply.Data), ServerNowTime: h.now().UnixMilli(), Notify: reply.Notify}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(answer); err != nil {
		h.logger().Error("write response", "error", fmt.Errorf("%s: %w", path, err))
	}
}

func elapsedMilliseconds(started time.Time) float64 {
	return float64(time.Since(started).Microseconds()) / 1000
}

func (h HTTP) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h HTTP) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

func (h HTTP) secureCookies() bool {
	return strings.EqualFold(h.Authentication.Mode, "oauth") &&
		strings.HasPrefix(strings.ToLower(h.Authentication.PublicURL), "https://")
}
