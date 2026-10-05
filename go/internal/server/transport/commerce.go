package transport

import (
	"encoding/json"
	"net/http"
)

func (h HTTP) clientCommerce(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	if h.CommerceManifest == nil {
		http.Error(w, "server purchases are unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(h.CommerceManifest()); err != nil {
		h.logger().Error("encode commerce manifest", "error", err)
	}
}
