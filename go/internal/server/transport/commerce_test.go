package transport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommerceManifestIsReadOnlyAndUnavailableWithoutPolicy(t *testing.T) {
	without := httptest.NewRecorder()
	HTTP{}.Handler().ServeHTTP(without, httptest.NewRequest(http.MethodGet, "/client/commerce", nil))
	if without.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing policy status=%d", without.Code)
	}
	calls := 0
	h := HTTP{CommerceManifest: func() any {
		calls++
		return map[string]any{"schema_version": 1, "products": []any{map[string]any{"sku": "test", "item_type": 2, "amount": 1000}}}
	}}.Handler()
	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/client/commerce", strings.NewReader(`{"amount":0}`)))
	if post.Code != http.StatusMethodNotAllowed || calls != 0 {
		t.Fatalf("policy accepted mutation: status=%d calls=%d", post.Code, calls)
	}
	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/client/commerce", nil))
	if get.Code != http.StatusOK || calls != 1 || get.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(get.Body.String(), `"amount":1000`) {
		t.Fatalf("manifest status=%d calls=%d headers=%v body=%s", get.Code, calls, get.Header(), get.Body.String())
	}
}
