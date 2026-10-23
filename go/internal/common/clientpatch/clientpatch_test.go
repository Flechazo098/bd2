package clientpatch

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"

	"bd2server/internal/common/release"
)

func signedFixture(t *testing.T, payload []byte, domain string) ([]byte, []release.TrustedKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := release.TrustedKey{ID: release.KeyID(public), PublicKey: base64.StdEncoding.EncodeToString(public)}
	raw, err := json.Marshal(Envelope{KeyID: key.ID, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, append([]byte(domain), payload...))), Plan: payload})
	if err != nil {
		t.Fatal(err)
	}
	return raw, []release.TrustedKey{key}
}

func TestOfficialSignatureBindsExactPayloadAndProtocolDomain(t *testing.T) {
	payload := []byte(`{"schema":1,"origin":"https://example.com","game_version":"2.36.7","database":"ClientLocal_v1","revision":1,"patches":[]}`)
	signed, keys := signedFixture(t, payload, SignatureDomain)
	if _, err := Verify(signed, keys); err != nil {
		t.Fatal(err)
	}
	altered := bytes.Replace(signed, []byte(`"revision":1`), []byte(`"revision":2`), 1)
	if _, err := Verify(altered, keys); err == nil {
		t.Fatal("modified revision retained signature authority")
	}
	_, otherKeys := signedFixture(t, payload, SignatureDomain)
	if _, err := Verify(signed, otherKeys); err == nil {
		t.Fatal("a different server key authorized an official plan")
	}
	otherDomain, otherDomainKeys := signedFixture(t, payload, "bd2-release-manifest-v1\n")
	if _, err := Verify(otherDomain, otherDomainKeys); err == nil {
		t.Fatal("release-manifest signature crossed the client-patch domain")
	}
}

func TestEncryptedFileOnlyAuthenticatesForRecipientAndUnmodifiedHeader(t *testing.T) {
	recipient, err := GenerateRecipient()
	if err != nil {
		t.Fatal(err)
	}
	other, err := GenerateRecipient()
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("a signed plan with public identifier corrections")
	first, err := Seal(payload, recipient.PublicKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Seal(payload, recipient.PublicKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("separate downloads reused the encrypted file")
	}
	for _, file := range [][]byte{first, second} {
		plain, err := Open(file, recipient)
		if err != nil || !bytes.Equal(plain, payload) {
			t.Fatalf("recipient could not recover plan: %v", err)
		}
		if _, err := Open(file, other); err == nil {
			t.Fatal("another launch recipient decrypted the plan")
		}
	}
	for _, offset := range []int{0, len(magic), len(magic) + 32, headerSize, len(first) - 1} {
		changed := bytes.Clone(first)
		changed[offset] ^= 1
		if _, err := Open(changed, recipient); err == nil {
			t.Fatalf("modified file at offset %d authenticated", offset)
		}
	}
	if _, err := Seal(payload, make([]byte, 32)); err == nil {
		t.Fatal("low-order X25519 recipient accepted")
	}
}

func TestSignedArgumentsRetainSQLiteIntegerIdentityAndRejectUnsafeScope(t *testing.T) {
	payload := []byte(`{"schema":1,"origin":"https://example.com","game_version":"2.36.7","database":"ClientLocal_v1","revision":1,"patches":[{"id":"identity-v1","owner_uid":30738202,"requires":[],"before":[{"sql":"SELECT count(*) FROM sqlite_schema WHERE name = :table","args":{"table":"Character"},"equals":1}],"statements":[{"sql":"UPDATE Character SET InstanceID = :new_id WHERE OwnerUID = :owner","args":{"new_id":9007199254740993}}],"after":[{"sql":"SELECT count(*) FROM Character WHERE OwnerUID = :owner AND InstanceID = :new_id","args":{"new_id":9007199254740993},"equals":1}]}]}`)
	signed, keys := signedFixture(t, payload, SignatureDomain)
	plan, err := Verify(signed, keys)
	if err != nil {
		t.Fatal(err)
	}
	number, ok := plan.Patches[0].Statements[0].Args["new_id"].(json.Number)
	if !ok || number.String() != "9007199254740993" {
		t.Fatalf("64-bit instance identity rounded during verification: %v", number)
	}
	plan.Patches[0].Statements[0].Args["owner"] = int64(1)
	if err := plan.Validate(); err == nil {
		t.Fatal("caller overrode trusted account scope")
	}
	delete(plan.Patches[0].Statements[0].Args, "owner")
	other := plan.Patches[0]
	other.ID = "other-account"
	other.OwnerUID++
	plan.Patches = append(plan.Patches, other)
	plan.Patches[0].Requires = []string{other.ID}
	if err := plan.Validate(); err == nil {
		t.Fatal("account patch depended on another player's patch")
	}
	plan.Patches[1].OwnerUID = 0
	if err := plan.Validate(); err != nil {
		t.Fatalf("account patch cannot depend on a global prerequisite: %v", err)
	}
	plan.Patches[1].OwnerUID = plan.Patches[0].OwnerUID
	plan.Patches[1].Requires = []string{plan.Patches[0].ID}
	if err := plan.Validate(); err == nil {
		t.Fatal("cyclic dependencies accepted")
	}
}

// Produced independently with Python cryptography's X25519, HKDF and AESGCM.
func TestOpenInteroperatesWithIndependentEncryptionVector(t *testing.T) {
	recipientBytes, err := hex.DecodeString("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := ecdh.X25519().NewPrivateKey(recipientBytes)
	if err != nil {
		t.Fatal(err)
	}
	file, err := hex.DecodeString("4244325041544348015869aff450549732cbaaed5e5df9b30a6da31cb0e5742bad5ad4a1a768f1a67b000102030405060708090a0b8c3af1692334c7c120059c22d38c3b0f68d4ddf161c98f003c395e9ab2c2b9db7c8412ceb15aa54df5ed3399f82c2e204d7f1b68108ef6560438a3958e343ba0a9fd97efd6")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Open(file, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != `{"official":"independent Python cryptography vector"}` {
		t.Fatalf("independent ciphertext decrypted incorrectly: %s", plain)
	}
}
