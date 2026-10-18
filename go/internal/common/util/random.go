package util

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/big"
)

func RandomUint64N(limit uint64) (uint64, error) {
	if limit == 0 {
		return 0, errors.New("util: random upper bound must be positive")
	}
	value, err := rand.Int(rand.Reader, new(big.Int).SetUint64(limit))
	if err != nil {
		return 0, err
	}
	return value.Uint64(), nil
}

func RandomHex(size int) (string, error) {
	if size < 0 {
		return "", errors.New("util: random byte count must not be negative")
	}
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func RandomBase64URL(size int) (string, error) {
	if size < 0 {
		return "", errors.New("util: random byte count must not be negative")
	}
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
