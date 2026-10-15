package i18n

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"sync"
)

//go:embed web/text.js
var browserScript []byte

func ServeBrowserScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write(browserScript)
}

type TextRequest struct {
	Locale   string        `json:"locale"`
	Messages []TextMessage `json:"messages"`
}

type TextMessage struct {
	Key  string `json:"key"`
	Args []any  `json:"args"`
}

func TextHandler(fsys fs.FS, directory string) http.HandlerFunc {
	load := sync.OnceValues(func() (*Catalog, error) { return Load(fsys, directory) })
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		var request TextRequest
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid text request", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF || !Supported(request.Locale) || len(request.Messages) == 0 || len(request.Messages) > 256 {
			http.Error(w, "invalid text request", http.StatusBadRequest)
			return
		}
		catalog, err := load()
		if err != nil {
			http.Error(w, "translation catalog unavailable", http.StatusInternalServerError)
			return
		}
		texts := make([]string, len(request.Messages))
		for index, message := range request.Messages {
			if len(message.Key) > 256 || len(message.Args) > 64 {
				http.Error(w, "invalid text message", http.StatusBadRequest)
				return
			}
			_, selected := catalog.entries[request.Locale][message.Key]
			_, fallback := catalog.entries[Fallback][message.Key]
			if !selected && !fallback {
				texts[index] = message.Key
				continue
			}
			for i, value := range message.Args {
				argument, err := nativeArgument(value)
				if err != nil {
					http.Error(w, "invalid text argument", http.StatusBadRequest)
					return
				}
				message.Args[i] = argument
			}
			texts[index] = catalog.Translate(request.Locale, message.Key, message.Args...)
			if len(texts[index]) > 64<<10 {
				http.Error(w, "formatted text is too long", http.StatusBadRequest)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(struct {
			Texts []string `json:"texts"`
		}{Texts: texts})
	}
}

func nativeArgument(value any) (any, error) {
	switch value := value.(type) {
	case string:
		if len(value) > 8192 {
			return nil, fmt.Errorf("i18n: text argument is too long")
		}
		return value, nil
	case nil, bool:
		return value, nil
	case json.Number:
		if integer, err := value.Int64(); err == nil {
			return integer, nil
		}
		return value.Float64()
	default:
		return nil, fmt.Errorf("i18n: argument must be a JSON primitive")
	}
}
