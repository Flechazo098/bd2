package release_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"

	"bd2server/internal/common/release"
)

func TestUpdateRequirementsCannotBeChangedWithoutPublisherSignature(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"server_version": "2.36.7+server.0.2.0", "client_version": "2.36.7+client.0.2.0", "require_client_update": true, "require_game_update": false, "minimum_plugins": map[string]string{"login_ui": "0.3.0"}}
	content, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	id := sha256.Sum256(content)
	payload["id"] = hex.EncodeToString(id[:])
	content, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(private, append([]byte("bd2-update-fingerprint-v1\n"), content...))
	envelope := map[string]any{"key_id": release.KeyID(public), "signature": base64.StdEncoding.EncodeToString(signature), "fingerprint": json.RawMessage(content)}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	trust := []release.TrustedKey{{ID: release.KeyID(public), PublicKey: base64.StdEncoding.EncodeToString(public)}}
	verified, err := release.VerifyFingerprint(raw, trust)
	if err != nil {
		t.Fatal(err)
	}
	old := release.ClientIdentity{Version: "2.36.7+client.0.1.0", Plugins: map[string]string{"login_ui": "0.2.0"}}
	if verified.Accepts(old) {
		t.Fatal("publisher's mandatory update did not exclude an old client")
	}
	payload["require_client_update"] = false
	delete(payload, "id")
	content, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	changedID := sha256.Sum256(content)
	payload["id"] = hex.EncodeToString(changedID[:])
	content, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	envelope["fingerprint"] = json.RawMessage(content)
	raw, err = json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := release.VerifyFingerprint(raw, trust); err == nil {
		t.Fatal("modified requirement was accepted with the publisher's old signature")
	}
	envelope["signature"] = base64.StdEncoding.EncodeToString(ed25519.Sign(private, append([]byte("bd2-update-fingerprint-v1\n"), content...)))
	raw, err = json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	modified, err := release.VerifyFingerprint(raw, trust)
	if err != nil {
		t.Fatalf("publisher cannot sign the valid modified contract: %v", err)
	}
	if !modified.Accepts(old) {
		t.Fatal("publisher-signed removal of the requirement did not admit the old client")
	}
}
