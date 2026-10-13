package admin

import (
	"context"
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

// Role is a console permission level.
type Role string

const (
	// RoleOwner manages the whole server. It belongs to the game's
	// administrator account and to sign-in links printed by the server.
	RoleOwner Role = "owner"
	// RoleMember may use the player tools for its own game account only.
	RoleMember Role = "member"
)

// Store persists console sessions and member grants in state/admin.db.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

type session struct {
	AccountID string
	Role      Role
	CSRF      string
	Expires   time.Time
}

const sessionTTL = 24 * time.Hour

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("admin: create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("admin: open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS sessions (token_hash BLOB PRIMARY KEY NOT NULL, account_id TEXT NOT NULL, role TEXT NOT NULL CHECK(role IN ('owner','member')), csrf TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS members (account_id TEXT PRIMARY KEY NOT NULL CHECK(length(account_id)>0), granted_at INTEGER NOT NULL) WITHOUT ROWID`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("admin: initialize database: %w", err)
		}
	}
	if err := os.Chmod(filepath.Clean(path), 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("admin: restrict database permissions: %w", err)
	}
	return &Store{db: db, now: time.Now}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// CreateSession returns a new session token and its CSRF token.
func (s *Store) CreateSession(ctx context.Context, accountID string, role Role) (token, csrf string, err error) {
	if token, err = randomToken(32); err != nil {
		return "", "", err
	}
	if csrf, err = randomToken(24); err != nil {
		return "", "", err
	}
	now := s.now()
	if _, err = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<=?`, now.Unix()); err != nil {
		return "", "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions VALUES(?,?,?,?,?,?)`, digest(token), accountID, string(role), csrf, now.Unix(), now.Add(sessionTTL).Unix())
	return token, csrf, err
}

// Session resolves a live session token. Member sessions end as soon as the
// grant is revoked, without waiting for expiry.
func (s *Store) Session(ctx context.Context, token string) (session, bool, error) {
	if token == "" {
		return session{}, false, nil
	}
	var result session
	var role string
	var expires int64
	err := s.db.QueryRowContext(ctx, `SELECT s.account_id,s.role,s.csrf,s.expires_at FROM sessions s
		WHERE s.token_hash=? AND s.expires_at>? AND (s.role='owner' OR EXISTS(SELECT 1 FROM members m WHERE m.account_id=s.account_id))`,
		digest(token), s.now().Unix()).Scan(&result.AccountID, &role, &result.CSRF, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return session{}, false, nil
	}
	if err != nil {
		return session{}, false, err
	}
	result.Role, result.Expires = Role(role), time.Unix(expires, 0)
	return result, true, nil
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, digest(token))
	return err
}

func (s *Store) Members(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT account_id FROM members`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	members := map[string]bool{}
	for rows.Next() {
		var account string
		if err := rows.Scan(&account); err != nil {
			return nil, err
		}
		members[account] = true
	}
	return members, rows.Err()
}

func (s *Store) IsMember(ctx context.Context, accountID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM members WHERE account_id=?)`, accountID).Scan(&exists)
	return exists, err
}

// SetMember grants or revokes member access. Revocation also ends sessions.
func (s *Store) SetMember(ctx context.Context, accountID string, member bool) error {
	if accountID == "" {
		return errors.New("admin: member requires an account")
	}
	if member {
		_, err := s.db.ExecContext(ctx, `INSERT INTO members VALUES(?,?) ON CONFLICT(account_id) DO NOTHING`, accountID, s.now().Unix())
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM members WHERE account_id=?`, accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE account_id=? AND role='member'`, accountID); err != nil {
		return err
	}
	return tx.Commit()
}

func randomToken(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func digest(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
