package clientpatch

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

const magic = "BD2PATCH\x01"
const headerSize = len(magic) + 32 + 12
const encryptionInfo = "bd2-client-patch-file-v1"

func GenerateRecipient() (*ecdh.PrivateKey, error) { return ecdh.X25519().GenerateKey(rand.Reader) }

func patchAEAD(private *ecdh.PrivateKey, public []byte) (cipher.AEAD, error) {
	peer, err := ecdh.X25519().NewPublicKey(public)
	if err != nil {
		return nil, fmt.Errorf("clientpatch: invalid recipient key: %w", err)
	}
	secret, err := private.ECDH(peer)
	if err != nil {
		return nil, fmt.Errorf("clientpatch: key agreement: %w", err)
	}
	defer clear(secret)
	key, err := hkdf.Key(sha256.New, secret, nil, encryptionInfo, 32)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func Seal(signed, recipientPublic []byte) ([]byte, error) {
	if len(signed) == 0 || len(signed) > MaxPlaintext {
		return nil, errors.New("clientpatch: plaintext size is invalid")
	}
	ephemeral, err := GenerateRecipient()
	if err != nil {
		return nil, err
	}
	aead, err := patchAEAD(ephemeral, recipientPublic)
	if err != nil {
		return nil, err
	}
	header := make([]byte, headerSize)
	copy(header, magic)
	copy(header[len(magic):], ephemeral.PublicKey().Bytes())
	nonce := header[len(magic)+32:]
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(header, nonce, signed, header), nil
}

func Open(file []byte, private *ecdh.PrivateKey) ([]byte, error) {
	if private == nil || private.Curve() != ecdh.X25519() || len(file) < headerSize+16 || len(file) > headerSize+MaxPlaintext+16 || !bytes.Equal(file[:len(magic)], []byte(magic)) {
		return nil, errors.New("clientpatch: invalid encrypted patch file")
	}
	header := file[:headerSize]
	aead, err := patchAEAD(private, header[len(magic):len(magic)+32])
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, header[len(magic)+32:], file[headerSize:], header)
	if err != nil {
		return nil, errors.New("clientpatch: encrypted patch authentication failed")
	}
	if len(plain) == 0 {
		return nil, errors.New("clientpatch: empty encrypted patch")
	}
	return plain, nil
}
