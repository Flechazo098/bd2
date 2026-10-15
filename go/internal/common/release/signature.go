package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// Signing domains keep a manifest signature from validating a changelog and
// the reverse.
const (
	signingDomain   = "bd2-release-manifest-v1\n"
	changelogDomain = "bd2-changelog-v1\n"
)

// ChangelogSignatureName is the detached signature published beside
// CHANGELOG.md; only notices it vouches for are shown as official.
const ChangelogSignatureName = "CHANGELOG.md.sig"

// Envelope is the published file. Manifest holds the exact signed bytes so
// that re-encoding can never change what was verified.
type Envelope struct {
	KeyID     string          `json:"key_id"`
	Signature string          `json:"signature"`
	Manifest  json.RawMessage `json:"manifest"`
}

// Channel is the built-in release source: where manifests and changelogs are
// published and which keys may sign a manifest.
type Channel struct {
	Name          string       `json:"name"`
	ManifestURL   string       `json:"manifest_url"`
	ChangelogURLs []string     `json:"changelog_urls"`
	TrustedKeys   []TrustedKey `json:"trusted_keys"`
}

type TrustedKey struct {
	ID        string `json:"id"`
	PublicKey string `json:"public_key"`
}

//go:embed channel.json
var channelJSON []byte

var builtin = sync.OnceValues(func() (Channel, error) {
	var c Channel
	decoder := json.NewDecoder(bytes.NewReader(channelJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return Channel{}, fmt.Errorf("release: built-in channel: %w", err)
	}
	for _, key := range c.TrustedKeys {
		if _, err := decodePublicKey(key); err != nil {
			return Channel{}, err
		}
	}
	return c, nil
})

// BuiltinChannel is the channel compiled into this binary.
func BuiltinChannel() Channel {
	c, err := builtin()
	if err != nil {
		panic(err)
	}
	return c
}

func decodePublicKey(key TrustedKey) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(key.PublicKey)
	if err != nil || len(raw) != ed25519.PublicKeySize || key.ID != KeyID(raw) {
		return nil, fmt.Errorf("release: trusted key %q is invalid", key.ID)
	}
	return ed25519.PublicKey(raw), nil
}

// KeyID names a public key by the first 16 hex digits of its SHA-256.
func KeyID(public []byte) string {
	sum := sha256.Sum256(public)
	return hex.EncodeToString(sum[:8])
}

// Detached is a signature over a file published separately, such as the
// changelog served from Git hosting.
type Detached struct {
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
}

// VerifyChangelog accepts raw only with a signature from a trusted key.
func VerifyChangelog(raw, signature []byte, keys []TrustedKey) error {
	var detached Detached
	if err := json.Unmarshal(signature, &detached); err != nil {
		return fmt.Errorf("release: changelog signature: %w", err)
	}
	public, err := trusted(detached.KeyID, keys)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(detached.Signature)
	if err != nil || !ed25519.Verify(public, append([]byte(changelogDomain), raw...), sig) {
		return errors.New("release: changelog signature does not match its content")
	}
	return nil
}

func trusted(id string, keys []TrustedKey) (ed25519.PublicKey, error) {
	for _, key := range keys {
		if key.ID == id {
			return decodePublicKey(key)
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("release: this build trusts no release key")
	}
	return nil, fmt.Errorf("release: signed by untrusted key %q", id)
}

// Verify checks raw against keys and returns the validated manifest.
func Verify(raw []byte, keys []TrustedKey) (Manifest, error) {
	var envelope Envelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Manifest{}, fmt.Errorf("release: manifest envelope: %w", err)
	}
	public, err := trusted(envelope.KeyID, keys)
	if err != nil {
		return Manifest{}, err
	}
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	if err != nil || !ed25519.Verify(public, append([]byte(signingDomain), envelope.Manifest...), signature) {
		return Manifest{}, errors.New("release: manifest signature is invalid")
	}
	var manifest Manifest
	decoder = json.NewDecoder(bytes.NewReader(envelope.Manifest))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("release: manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}
