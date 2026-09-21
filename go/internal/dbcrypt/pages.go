// Package dbcrypt implements the page cipher shared by Intro and GameData
// SQLite files in Brown Dust II 2.34.13.
package dbcrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"fmt"
)

const PageSize = 4096

var Header = []byte("SQLite format 3\x00")

func DecryptPages(in []byte) ([]byte, error) { return cryptPages(in, false) }
func EncryptPages(in []byte) ([]byte, error) { return cryptPages(in, true) }

func cryptPages(in []byte, encrypt bool) ([]byte, error) {
	if len(in) == 0 || len(in)%PageSize != 0 {
		return nil, fmt.Errorf("dbcrypt: database length %d is not a non-zero multiple of %d", len(in), PageSize)
	}
	block, err := aes.NewCipher(deriveKey())
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(in))
	for start := 0; start < len(in); start += PageSize {
		var mode cipher.BlockMode = cipher.NewCBCEncrypter(block, Header)
		if !encrypt {
			mode = cipher.NewCBCDecrypter(block, Header)
		}
		mode.CryptBlocks(out[start:start+PageSize], in[start:start+PageSize])
	}
	return out, nil
}

func deriveKey() []byte {
	password := []byte(fmt.Sprintf("%X", sha1.Sum([]byte("spdhdnlwmrpavmtm"))))
	return pbkdf2SHA1(password, Header, 2010, 32)
}

func pbkdf2SHA1(password, salt []byte, iterations, length int) []byte {
	var result []byte
	for block := uint32(1); len(result) < length; block++ {
		message := append(append([]byte{}, salt...), byte(block>>24), byte(block>>16), byte(block>>8), byte(block))
		u := hmacSHA1(password, message)
		t := append([]byte{}, u...)
		for i := 1; i < iterations; i++ {
			u = hmacSHA1(password, u)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		result = append(result, t...)
	}
	return result[:length]
}

func hmacSHA1(key, message []byte) []byte {
	h := hmac.New(sha1.New, key)
	_, _ = h.Write(message)
	return h.Sum(nil)
}
