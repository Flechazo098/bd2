package transport

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"bd2server/internal/bootstrap"
	"bd2server/internal/wire"
)

func TestBootstrapRoundTrip(t *testing.T) {
	cfg := bootstrap.Config{BaseURL: "http://127.0.0.1:8080/game/", CDNURL: "http://127.0.0.1:8080/assets/ServerData", Version: bootstrap.ClientVersion, BundleVer: bootstrap.BundleVersion}
	now := func() time.Time { return time.UnixMilli(12345) }
	h := HTTP{Dispatcher: Bootstrap{Config: cfg, Now: now}, Now: now}.Handler()
	for _, path := range []string{"/MaintenanceInfo", "/ServerInfo", "/NoticeInfo", "/ServerNowTime"} {
		request := base64.StdEncoding.EncodeToString(wire.AppendVarint(nil, 1, 2))
		req := httptest.NewRequest(http.MethodPut, "/game"+path, strings.NewReader(request))
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, res.Code, res.Body.String())
		}
		var envelope Envelope
		if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil || envelope.ErrorType != 0 || envelope.ServerNowTime != 12345 {
			t.Fatalf("%s: %v %+v", path, err, envelope)
		}
		if _, err := base64.StdEncoding.DecodeString(envelope.Data); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

func TestUnknownPacketFailsClosed(t *testing.T) {
	h := HTTP{Dispatcher: Bootstrap{}}.Handler()
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodPut, "/game/InventedPacket", strings.NewReader("AA==")))
	if res.Code != http.StatusNotImplemented {
		t.Fatalf("unknown packet status: %d", res.Code)
	}
}

func TestGameDataStaticFiles(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/123/release"
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"/common-dbdata.info", []byte("42"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := HTTP{GameDataDir: dir}.Handler()
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/assets/GameData/123/release/common-dbdata.info", nil))
	if res.Code != http.StatusOK || res.Body.String() != "42" {
		t.Fatalf("static GameData: status=%d body=%q", res.Code, res.Body.String())
	}
}
