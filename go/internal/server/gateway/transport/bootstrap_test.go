package transport

import (
	"bytes"
	"encoding/base64"
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
