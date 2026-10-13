// Package configapi exposes a config.Registry as a small JSON HTTP API. It
// knows nothing about the documents it serves; authentication, authorization
// and CSRF protection belong to the caller that mounts the handler.
package configapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"bd2server/internal/common/config"
)

const maxDocumentBytes = 1 << 20

type Options struct {
	// Changed is called after a document was written successfully.
	Changed func(r *http.Request, view config.View)
}

type document struct {
	config.Description
	View config.View `json:"view"`
}

// Handler serves, relative to its mount point:
//
//	GET  /              every document with its description and current view
//	GET  /{name}        one document
//	POST /{name}/check  validate a candidate without writing it
//	PUT  /{name}        replace the document; If-Match carries the revision read
func Handler(registry *config.Registry, options Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		entries := registry.Entries()
		documents := make([]document, 0, len(entries))
		for _, entry := range entries {
			item, err := describe(entry)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			documents = append(documents, item)
		}
		writeJSON(w, http.StatusOK, map[string]any{"documents": documents})
	})
	mux.HandleFunc("GET /{name}", func(w http.ResponseWriter, r *http.Request) {
		entry, ok := lookup(w, registry, r)
		if !ok {
			return
		}
		item, err := describe(entry)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})
	mux.HandleFunc("POST /{name}/check", func(w http.ResponseWriter, r *http.Request) {
		entry, ok := lookup(w, registry, r)
		if !ok {
			return
		}
		raw, ok := readBody(w, r)
		if !ok {
			return
		}
		problems, advice := entry.CheckJSON(raw)
		writeJSON(w, http.StatusOK, map[string]any{"problems": nonNil(problems), "advice": nonNil(advice)})
	})
	mux.HandleFunc("PUT /{name}", func(w http.ResponseWriter, r *http.Request) {
		entry, ok := lookup(w, registry, r)
		if !ok {
			return
		}
		expected := config.Revision(r.Header.Get("If-Match"))
		if expected == "" {
			writeJSON(w, http.StatusPreconditionRequired, map[string]string{"error": "If-Match revision required"})
			return
		}
		raw, ok := readBody(w, r)
		if !ok {
			return
		}
		view, err := entry.ReplaceJSON(raw, expected)
		var problems config.Problems
		switch {
		case err == nil:
			if options.Changed != nil {
				options.Changed(r, view)
			}
			writeJSON(w, http.StatusOK, view)
		case errors.Is(err, config.ErrConflict):
			current, viewErr := entry.View()
			if viewErr != nil {
				writeError(w, http.StatusInternalServerError, viewErr)
				return
			}
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "view": current})
		case errors.Is(err, config.ErrReadOnly):
			writeError(w, http.StatusForbidden, err)
		case errors.As(err, &problems):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "invalid configuration", "problems": problems})
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
	})
	return mux
}

func describe(entry config.Entry) (document, error) {
	description, err := entry.Describe()
	if err != nil {
		return document{}, err
	}
	view, err := entry.View()
	return document{Description: description, View: view}, err
}

func lookup(w http.ResponseWriter, registry *config.Registry, r *http.Request) (config.Entry, bool) {
	entry, ok := registry.Lookup(r.PathValue("name"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown configuration"})
	}
	return entry, ok
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxDocumentBytes+1))
	if err != nil || len(raw) > maxDocumentBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "configuration body too large"})
		return nil, false
	}
	return raw, true
}

func nonNil(problems config.Problems) config.Problems {
	if problems == nil {
		return config.Problems{}
	}
	return problems
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
