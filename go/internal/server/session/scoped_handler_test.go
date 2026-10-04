package session

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"bd2server/internal/server/cryptox"
	"bd2server/internal/server/protocol"
	"bd2server/internal/server/wire"
)

type scopedHandler struct{ sessions []string }

func (h *scopedHandler) Handle(string, []byte) (int, []byte, bool, error) {
	panic("session handler fell back to unscoped dispatch")
}
func (h *scopedHandler) HandleSession(path string, request []byte, sessionID string) (int, []byte, bool, error) {
	h.sessions = append(h.sessions, sessionID)
	return 77, nil, true, nil
}

func TestScopedDispatchUsesAuthorizedSessionInsideBatch(t *testing.T) {
	h := &scopedHandler{}
	s, err := NewServer(fakeLogin{}, h)
	if err != nil {
		t.Fatal(err)
	}
	first := login(t, s)
	firstGame := s.sessions[sessionTokenKey(first.Cookie)]
	second := login(t, s)
	secondGame := s.sessions[sessionTokenKey(second.Cookie)]
	for _, test := range []struct {
		cookie string
		game   *gameSession
	}{{first.Cookie, firstGame}, {second.Cookie, secondGame}, {first.Cookie, firstGame}} {
		requests := []protocol.BatchRequest{{Path: "/Scoped", RequestData: base64.StdEncoding.EncodeToString(wire.AppendVarint(nil, 1, 2))}, {Path: "/ScopedSecond", RequestData: base64.StdEncoding.EncodeToString(wire.AppendVarint(nil, 1, 3))}}
		plain, _ := json.Marshal(requests)
		body, _ := cryptox.EncryptBase64(plain, test.game.key)
		if _, err := s.DispatchRaw("/BatchRequest", []byte(body), "s="+test.cookie); err != nil {
			t.Fatal(err)
		}
		for _, got := range h.sessions[len(h.sessions)-2:] {
			if got != test.game.id {
				t.Fatalf("session %q want %q", got, test.game.id)
			}
		}
	}
}
