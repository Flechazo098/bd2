package transport

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An idle game client only polls the plaintext /ServerNowTime, so the
// server-wide Notify (maintenance start, notice sequence) must ride on that
// reply, not only on gameplay requests an idle player never sends.
func TestServerNowTimeCarriesTheBroadcastNotify(t *testing.T) {
	broadcast := []byte{0x38, 0x2a}
	b := Bootstrap{Broadcast: func() []byte { return broadcast }}
	reply, err := b.Dispatch("/ServerNowTime", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(reply.Notify)
	if err != nil || !bytes.Equal(raw, broadcast) {
		t.Fatalf("ServerNowTime Notify = %q (%v), want base64 of the broadcast fields", reply.Notify, err)
	}
}

func TestTimeHeartbeatRecordsActivityOnlyAfterSuccessfulDispatch(t *testing.T) {
	calls := 0
	broadcast := []byte{0x38, 0x2a}
	handler := HTTP{Dispatcher: Bootstrap{Broadcast: func() []byte { return broadcast }}, SessionActivity: func(cookie string) {
		calls++
		if cookie != "s=heartbeat" && cookie != "" {
			t.Fatalf("unexpected cookie %q", cookie)
		}
	}}.Handler()
	for _, tc := range []struct {
		path, body, cookie string
		status, activity   int
	}{
		{"/game/ServerNowTime", "", "s=heartbeat", http.StatusOK, 1},
		{"/game/ServerNowTime", "", "", http.StatusOK, 1},
		{"/game/ServerNowTime", "!invalid!", "s=heartbeat", http.StatusBadRequest, 0},
		{"/game/NoticeInfo", "", "s=heartbeat", http.StatusOK, 0},
	} {
		before := calls
		request := httptest.NewRequest(http.MethodPut, tc.path, strings.NewReader(tc.body))
		request.Header.Set("Cookie", tc.cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != tc.status || calls-before != tc.activity {
			t.Fatalf("%s: status=%d activity=%d", tc.path, response.Code, calls-before)
		}
		if tc.status == http.StatusOK && tc.path == "/game/ServerNowTime" {
			var answer Envelope
			if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
				t.Fatal(err)
			}
			notify, err := base64.StdEncoding.DecodeString(answer.Notify)
			if err != nil || !bytes.Equal(notify, broadcast) {
				t.Fatal("heartbeat lost maintenance notification")
			}
		}
	}
}
