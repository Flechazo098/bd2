package transport

import (
	"encoding/json"
	"net/http"
)

func (h HTTP) withCompatibility(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.ClientAllowed != nil && !h.ClientAllowed(r.Header) {
			updateRequired(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func updateRequired(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-BD2-Update-Required", "1")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": "client_update_required"})
}
