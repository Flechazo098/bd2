package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 2

var (
	ErrUnauthorized = errors.New("auth: unauthorized")
	ErrExpired      = errors.New("auth: expired")
	ErrPending      = errors.New("auth: pending")
	ErrConsumed     = errors.New("auth: consumed")
	ErrNotAllowed   = errors.New("auth: identity is not allowed on this single-owner server")
)

type Store struct {
	db      *sql.DB
	hashKey []byte
	aead    cipher.AEAD
	now     func() time.Time
}

func Open(path string, masterKey []byte) (*Store, error) {
	if len(masterKey) != 32 {
		return nil, errors.New("auth: master key must contain 32 bytes")
	}
	defer clear(masterKey)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("auth: create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("auth: open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	opened := false
	defer func() {
		if !opened {
			_ = db.Close()
		}
	}()
	if _, err = db.Exec(`PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000`); err != nil {
		return nil, fmt.Errorf("auth: configure database: %w", err)
	}
	if err = os.Chmod(filepath.Clean(path), 0o600); err != nil {
		return nil, fmt.Errorf("auth: restrict database permissions: %w", err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY NOT NULL, value TEXT NOT NULL) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS accounts (id TEXT PRIMARY KEY NOT NULL, status TEXT NOT NULL, created_at INTEGER NOT NULL, last_login_at INTEGER NOT NULL) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS identities (provider TEXT NOT NULL, issuer TEXT NOT NULL, subject_hash BLOB NOT NULL, account_id TEXT NOT NULL REFERENCES accounts(id), created_at INTEGER NOT NULL, last_login_at INTEGER NOT NULL, PRIMARY KEY(issuer,subject_hash)) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS devices (id TEXT PRIMARY KEY NOT NULL, client_hash BLOB NOT NULL, secret_hash BLOB NOT NULL, start_hash BLOB NOT NULL, provider TEXT NOT NULL, state_hash BLOB, verifier_cipher BLOB, nonce_cipher BLOB, result_cipher BLOB, status TEXT NOT NULL, error_code TEXT, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS devices_client_pending ON devices(client_hash,status,expires_at)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS devices_state ON devices(state_hash) WHERE state_hash IS NOT NULL`,
		`CREATE TABLE IF NOT EXISTS families (id TEXT PRIMARY KEY NOT NULL, account_id TEXT NOT NULL REFERENCES accounts(id), provider TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, revoked_at INTEGER) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS refresh_tokens (token_hash BLOB PRIMARY KEY NOT NULL, family_id TEXT NOT NULL REFERENCES families(id), created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, used_at INTEGER, revoked_at INTEGER) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS access_tokens (token_hash BLOB PRIMARY KEY NOT NULL, family_id TEXT NOT NULL REFERENCES families(id), account_id TEXT NOT NULL REFERENCES accounts(id), created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, revoked_at INTEGER) WITHOUT ROWID`,
	}
	for _, statement := range statements {
		if _, err = tx.Exec(statement); err != nil {
			return nil, fmt.Errorf("auth: create schema: %w", err)
		}
	}
	var version int
	err = tx.QueryRow(`SELECT CAST(value AS INTEGER) FROM metadata WHERE key='schema_version'`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = tx.Exec(`INSERT INTO metadata(key,value) VALUES('schema_version',1)`); err != nil {
			return nil, err
		}
		version = 1
	} else if err != nil {
		return nil, err
	}
	if version < 1 || version > schemaVersion {
		return nil, fmt.Errorf("auth: schema version %d, want %d", version, schemaVersion)
	}
	for version < schemaVersion {
		switch version {
		case 1:
			if _, err = tx.Exec(`CREATE TABLE refresh_attempts (
				family_id TEXT NOT NULL REFERENCES families(id),
				attempt_hash BLOB NOT NULL,
				request_token_hash BLOB NOT NULL,
				result_cipher BLOB NOT NULL,
				created_at INTEGER NOT NULL,
				expires_at INTEGER NOT NULL,
				PRIMARY KEY(family_id,attempt_hash)
			) WITHOUT ROWID`); err != nil {
				return nil, fmt.Errorf("auth: migrate schema 1->2: %w", err)
			}
			version = 2
		default:
			return nil, fmt.Errorf("auth: missing adjacent migration %d->%d", version, version+1)
		}
		if _, err = tx.Exec(`UPDATE metadata SET value=? WHERE key='schema_version'`, version); err != nil {
			return nil, fmt.Errorf("auth: record schema version %d: %w", version, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	encryptionKey := derive(masterKey, "auth-encryption")
	hashKey := derive(masterKey, "auth-token-hmac")
	block, err := aes.NewCipher(encryptionKey)
	clear(encryptionKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	opened = true
	return &Store{db: db, hashKey: hashKey, aead: aead, now: time.Now}, nil
}

func (s *Store) Close() error {
	clear(s.hashKey)
	return s.db.Close()
}

func derive(master []byte, purpose string) []byte {
	mac := hmac.New(sha256.New, master)
	_, _ = mac.Write([]byte("bd2/" + purpose + "/v1"))
	return mac.Sum(nil)
}

func (s *Store) digest(purpose, raw string) []byte {
	mac := hmac.New(sha256.New, s.hashKey)
	_, _ = mac.Write([]byte(purpose))
	_, _ = mac.Write([]byte{'\x00'})
	_, _ = mac.Write([]byte(raw))
	return mac.Sum(nil)
}

func (s *Store) identityDigest(issuer, subject string) []byte {
	mac := hmac.New(sha256.New, s.hashKey)
	_, _ = mac.Write([]byte("identity\x00"))
	_, _ = mac.Write([]byte(issuer))
	_, _ = mac.Write([]byte{'\x00'})
	_, _ = mac.Write([]byte(subject))
	return mac.Sum(nil)
}

func randomToken(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (s *Store) seal(id, field string, plain []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	aad := []byte("bd2/auth/v1/" + id + "/" + field)
	return s.aead.Seal(nonce, nonce, plain, aad), nil
}

func (s *Store) open(id, field string, sealed []byte) ([]byte, error) {
	if len(sealed) < s.aead.NonceSize() {
		return nil, errors.New("auth: invalid ciphertext")
	}
	nonce, ciphertext := sealed[:s.aead.NonceSize()], sealed[s.aead.NonceSize():]
	return s.aead.Open(nil, nonce, ciphertext, []byte("bd2/auth/v1/"+id+"/"+field))
}
