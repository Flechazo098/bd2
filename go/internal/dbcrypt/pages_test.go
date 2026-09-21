package dbcrypt

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	plain := make([]byte, PageSize*2)
	copy(plain, Header)
	copy(plain[PageSize:], []byte("second independent page"))
	encrypted, err := EncryptPages(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(encrypted, plain) {
		t.Fatal("encryption did not change data")
	}
	decrypted, err := DecryptPages(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted, plain) {
		t.Fatal("round trip changed pages")
	}
}
