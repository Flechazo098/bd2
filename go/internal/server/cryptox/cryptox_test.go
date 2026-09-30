package cryptox

import (
	"bytes"
	"testing"
)

func TestAESRoundTripAndFixedKey(t *testing.T) {
	plain := []byte("BD2 AES fixture: blocks and PKCS7")
	encoded, err := EncryptBase64(plain, Key())
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecryptBase64(encoded, Key())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round trip = %q, want %q", got, plain)
	}
}

func TestSessionKeyValidation(t *testing.T) {
	key, err := SessionKey("7eeb5c2480c298f6fc4c00d22b1b0b15")
	if err != nil || string(key) != "7eeb5c2480c298f6fc4c00d22b1b0b15" {
		t.Fatalf("SessionKey = %q, %v", key, err)
	}
	if _, err := SessionKey("not-a-key"); err == nil {
		t.Fatal("SessionKey accepted invalid key")
	}
}

func TestRejectBadPadding(t *testing.T) {
	if _, err := Decrypt(make([]byte, 16), Key()); err == nil {
		t.Fatal("Decrypt accepted bad padding")
	}
}
