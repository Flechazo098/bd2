// Package cryptox implements the small AES envelope used by the BD2 game API.
//
// The protocol key is deliberately represented as its ASCII bytes: a session
// key such as "7eeb...0b15" is not hex-decoded before it is supplied to AES.
package cryptox

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"fmt"
)

const (
	// FixedLoginKey encrypts LoginUser and JoinUser before a user_key exists.
	FixedLoginKey = "abcdefghijkrstuv024680wxyzlmnopq"
	// AESKeySize is the wire protocol key size (AES-256).
	AESKeySize = 32
)

var zeroIV [aes.BlockSize]byte

var (
	ErrInvalidKey        = errors.New("bd2 crypto: key must be exactly 32 ASCII bytes")
	ErrInvalidCiphertext = errors.New("bd2 crypto: ciphertext must be a non-empty AES block sequence")
	ErrInvalidPadding    = errors.New("bd2 crypto: invalid PKCS#7 padding")
)

// Key returns the fixed pre-login AES key.
func Key() []byte { return []byte(FixedLoginKey) }

// SessionKey validates a login user_key. The protocol passes its 32 ASCII
// characters directly to AES; it does not decode the hexadecimal spelling.
func SessionKey(userKey string) ([]byte, error) {
	if len(userKey) != AESKeySize {
		return nil, fmt.Errorf("%w: got %d bytes", ErrInvalidKey, len(userKey))
	}
	for _, c := range []byte(userKey) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return nil, fmt.Errorf("%w: session key is not hexadecimal ASCII", ErrInvalidKey)
		}
	}
	return []byte(userKey), nil
}

// Encrypt applies PKCS#7 and AES-256-CBC using the all-zero protocol IV.
func Encrypt(plain, key []byte) ([]byte, error) {
	if len(key) != AESKeySize {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padded := pad(plain, block.BlockSize())
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, zeroIV[:]).CryptBlocks(out, padded)
	return out, nil
}

// Decrypt removes AES-256-CBC and requires valid PKCS#7 padding.
func Decrypt(ciphertext, key []byte) ([]byte, error) {
	if len(key) != AESKeySize {
		return nil, ErrInvalidKey
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, ErrInvalidCiphertext
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, zeroIV[:]).CryptBlocks(out, ciphertext)
	return unpad(out, block.BlockSize())
}

// EncryptBase64 returns the API's AES transport value.
func EncryptBase64(plain, key []byte) (string, error) {
	ciphertext, err := Encrypt(plain, key)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptBase64 accepts the API's base64(AES-CBC(...)) transport value.
func DecryptBase64(encoded string, key []byte) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("bd2 crypto: invalid base64 ciphertext: %w", err)
	}
	return Decrypt(ciphertext, key)
}

// EncryptBase64Payload is the game-message envelope transform. A protobuf
// payload is first base64 encoded, then AES-CBC encrypted, then base64 encoded
// for JSON transport.
func EncryptBase64Payload(payload, key []byte) (string, error) {
	return EncryptBase64([]byte(base64.StdEncoding.EncodeToString(payload)), key)
}

// DecryptBase64Payload reverses EncryptBase64Payload and returns protobuf
// bytes directly.
func DecryptBase64Payload(encoded string, key []byte) ([]byte, error) {
	inner, err := DecryptBase64(encoded, key)
	if err != nil {
		return nil, err
	}
	payload, err := base64.StdEncoding.DecodeString(string(inner))
	if err != nil {
		return nil, fmt.Errorf("bd2 crypto: invalid inner base64 payload: %w", err)
	}
	return payload, nil
}

func pad(src []byte, blockSize int) []byte {
	n := blockSize - len(src)%blockSize
	out := make([]byte, len(src)+n)
	copy(out, src)
	copy(out[len(src):], bytes.Repeat([]byte{byte(n)}, n))
	return out
}

func unpad(src []byte, blockSize int) ([]byte, error) {
	if len(src) == 0 || len(src)%blockSize != 0 {
		return nil, ErrInvalidPadding
	}
	n := int(src[len(src)-1])
	if n == 0 || n > blockSize || n > len(src) || !bytes.Equal(src[len(src)-n:], bytes.Repeat([]byte{byte(n)}, n)) {
		return nil, ErrInvalidPadding
	}
	return src[:len(src)-n], nil
}
