package app

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/world"
	"bd2server/internal/server/gateway/session"
	"bd2server/internal/server/runtime/player"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

type storyCheckpoints struct {
	game    *session.Server
	players *playerRegistry
}

func (h storyCheckpoints) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(body) > 4096 {
		http.Error(w, "invalid story checkpoint request", http.StatusBadRequest)
		return
	}
	var request world.StoryCheckpointRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&request); err != nil {
		http.Error(w, "invalid story checkpoint request", http.StatusBadRequest)
		return
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid story checkpoint request", http.StatusBadRequest)
		return
	}
	principal, err := h.game.Principal(r.Context(), r.Header.Get("Cookie"))
	if err != nil {
		slog.Warn("Story checkpoint request failed", "phase", "authenticate", "operation", request.Operation, "error", err)
		http.Error(w, "game session required", http.StatusUnauthorized)
		return
	}
	runtime, release, err := h.players.Acquire(r.Context(), principal.AccountID)
	if err != nil {
		slog.Warn("Story checkpoint request failed", "phase", "acquire", "operation", request.Operation, "error", err)
		http.Error(w, "player unavailable", http.StatusServiceUnavailable)
		return
	}
	defer release()
	identity := command.Identity{AccountID: principal.AccountID, SessionID: principal.SessionID, RequestID: fmt.Sprintf("story-checkpoint:%d", systemSequence.Add(1))}
	future, err := runtime.Submit(r.Context(), player.Command{Identity: identity, ExpectedGeneration: principal.Generation, Digest: sha256.Sum256(body), System: &player.SystemRequest{Kind: player.StoryCheckpoint, Body: body}})
	if err != nil {
		slog.Warn("Story checkpoint request failed", "phase", "submit", "operation", request.Operation, "error", err)
		http.Error(w, "player unavailable", http.StatusServiceUnavailable)
		return
	}
	reply, err := future.Wait(r.Context())
	if err != nil {
		slog.Warn("Story checkpoint request failed", "phase", "execute", "operation", request.Operation, "error", err)
		status := http.StatusServiceUnavailable
		if errors.Is(err, world.ErrInvalidRequest) {
			status = http.StatusBadRequest
		}
		if errors.Is(err, world.ErrStoryCheckpointConflict) {
			status = http.StatusConflict
		}
		http.Error(w, "story checkpoint unavailable", status)
		return
	}
	if len(reply.Responses) != 1 {
		slog.Warn("Story checkpoint request failed", "phase", "response", "operation", request.Operation, "response_count", len(reply.Responses))
		http.Error(w, "invalid story checkpoint response", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(reply.Responses[0].Body)
}
