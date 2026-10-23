package clientpatch

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	patch "bd2server/internal/common/clientpatch"
	"bd2server/internal/common/release"
)

type Config struct {
	Root        string
	Origin      string
	GameVersion string
	Keys        []release.TrustedKey
}

func New(config Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request struct {
			RecipientKey string `json:"recipient_key"`
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 2049))
		if err != nil || len(raw) > 2048 {
			http.Error(w, "invalid patch request size", http.StatusBadRequest)
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid patch request", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			http.Error(w, "invalid patch request", http.StatusBadRequest)
			return
		}
		public, err := base64.StdEncoding.DecodeString(request.RecipientKey)
		if err != nil || len(public) != 32 {
			http.Error(w, "invalid recipient key", http.StatusBadRequest)
			return
		}
		file, err := os.Open(filepath.Join(config.Root, "plan.json"))
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			slog.Warn("client patch plan unavailable", "error", err)
			http.Error(w, "the server cannot open its client patch plan (data/client-patches/plan.json)", http.StatusServiceUnavailable)
			return
		}
		signed, readErr := io.ReadAll(io.LimitReader(file, patch.MaxPlaintext+1))
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			slog.Warn("client patch plan unreadable", "error", err)
			http.Error(w, "the server cannot read its client patch plan (data/client-patches/plan.json)", http.StatusServiceUnavailable)
			return
		}
		plan, err := patch.Verify(signed, config.Keys)
		if err != nil {
			err = fmt.Errorf("the server's client patch plan failed signature verification: %w", err)
		} else if plan.Origin != config.Origin || plan.GameVersion != config.GameVersion {
			err = fmt.Errorf("the server's client patch plan targets game %s at %s, but this server runs game %s at %s; the operator must re-sign data/client-patches/plan.json",
				plan.GameVersion, plan.Origin, config.GameVersion, config.Origin)
		}
		if err != nil {
			slog.Warn("client patch plan rejected", "error", err)
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		encrypted, err := patch.Seal(signed, public)
		if err != nil {
			http.Error(w, "recipient key cannot be used", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.bd2.client-patch")
		w.Header().Set("Content-Disposition", `attachment; filename="client.bdpatch"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(encrypted)
	})
}
