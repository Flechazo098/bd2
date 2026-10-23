package clientpatch

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	patch "bd2server/internal/common/clientpatch"
	"bd2server/internal/common/release"
)

func TestDownloadRequiresOfficialAuthorityAndExactServerScope(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := release.TrustedKey{ID: release.KeyID(public), PublicKey: base64.StdEncoding.EncodeToString(public)}
	root := t.TempDir()
	config := Config{Root: root, Origin: "https://example.com", GameVersion: "2.36.7", Keys: []release.TrustedKey{key}}
	handler := New(config)
	recipient, err := patch.GenerateRecipient()
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"recipient_key": base64.StdEncoding.EncodeToString(recipient.PublicKey().Bytes())})
	if err != nil {
		t.Fatal(err)
	}
	download := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/client/patches", bytes.NewReader(body)))
		return w
	}
	if w := download(); w.Code != http.StatusNotFound {
		t.Fatalf("missing publication was not distinguishable: %d", w.Code)
	}
	plan := patch.Plan{Schema: 1, Origin: config.Origin, GameVersion: config.GameVersion, Database: patch.Database, Revision: 1, Patches: []patch.Patch{}}
	publish := func() []byte {
		payload, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		signed, err := json.Marshal(patch.Envelope{KeyID: key.ID, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, append([]byte(patch.SignatureDomain), payload...))), Plan: payload})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "plan.json"), signed, 0600); err != nil {
			t.Fatal(err)
		}
		return signed
	}
	signed := publish()
	first, second := download(), download()
	for _, w := range []*httptest.ResponseRecorder{first, second} {
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/vnd.bd2.client-patch" {
			t.Fatalf("patch download failed: %d %s", w.Code, w.Body.String())
		}
		plain, err := patch.Open(w.Body.Bytes(), recipient)
		if err != nil || !bytes.Equal(plain, signed) {
			t.Fatalf("download did not preserve signed bytes: %v", err)
		}
		if _, err := patch.Verify(plain, config.Keys); err != nil {
			t.Fatal(err)
		}
	}
	if bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatal("downloads reused recipient ciphertext")
	}
	plan.Origin = "https://another-server.example"
	publish()
	if w := download(); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("plan for another origin served: %d", w.Code)
	}
	plan.Origin = config.Origin
	plan.GameVersion = "2.36.8"
	publish()
	if w := download(); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "targets game 2.36.8") || !strings.Contains(w.Body.String(), "runs game 2.36.7") {
		t.Fatalf("plan for another game served or unexplained: %d %q", w.Code, w.Body.String())
	}
	plan.GameVersion = config.GameVersion
	signed = publish()
	signed = bytes.Replace(signed, []byte(`"revision":1`), []byte(`"revision":2`), 1)
	if err := os.WriteFile(filepath.Join(root, "plan.json"), signed, 0600); err != nil {
		t.Fatal(err)
	}
	if w := download(); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("modified publication served: %d", w.Code)
	}
}
