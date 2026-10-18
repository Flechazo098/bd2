package app

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/runtime/player"
	"bd2server/internal/server/storage/stateio"
)

const spineRecordingPath = "/client/spine-recordings/"

type spineRecordingDownload struct {
	ID    uint64 `json:"id"`
	Token string `json:"token"`
}

func (p *playerAssembly) interactionRecords() roster.InteractionRecordService {
	return roster.InteractionRecordService{
		Store: stateio.RootStore{}, Design: p.design.interaction,
		Authorize: p.interactionAvailable, CheckText: p.checkProfileText,
		RecordDataURL: func(ctx command.Context, id uint64, token string) (string, error) {
			if ctx.AccountID == "" {
				return "", errors.New("recording account identity is missing")
			}
			account := base64.RawURLEncoding.EncodeToString([]byte(ctx.AccountID))
			base := p.options.gameRules.SpineRecording.DownloadBaseURL
			if base == "" {
				base = strings.TrimRight(consoleOrigin(p.options), "/") + strings.TrimRight(spineRecordingPath, "/")
			}
			return strings.TrimRight(base, "/") + "/" + account + "/" + strconv.FormatUint(id, 10) + "/" + token, nil
		},
	}
}

type spineRecordings struct{ players *playerRegistry }

func (h spineRecordings) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, spineRecordingPath), "/")
	if !strings.HasPrefix(r.URL.Path, spineRecordingPath) || len(parts) != 3 || len(parts[0]) > 512 || len(parts[2]) != 43 {
		http.NotFound(w, r)
		return
	}
	account, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(account) == 0 || !utf8.Valid(account) || base64.RawURLEncoding.EncodeToString(account) != parts[0] {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseUint(parts[1], 10, 63)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != parts[1] {
		http.NotFound(w, r)
		return
	}
	if token, err := base64.RawURLEncoding.DecodeString(parts[2]); err != nil || len(token) != 32 || base64.RawURLEncoding.EncodeToString(token) != parts[2] {
		http.NotFound(w, r)
		return
	}
	statePath := filepath.Join(h.players.factory.options.stateDirectory, "accounts", accountDirectoryName(string(account)), "state.db")
	if _, err := os.Stat(statePath); errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "recording unavailable", http.StatusServiceUnavailable)
		return
	}
	request, err := json.Marshal(spineRecordingDownload{ID: id, Token: parts[2]})
	if err != nil {
		http.Error(w, "recording unavailable", http.StatusServiceUnavailable)
		return
	}
	body, err := executePlayerSystem(r.Context(), h.players, string(account), player.ReadSpineRecording, request)
	if errors.Is(err, roster.ErrRecordNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "recording unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}
