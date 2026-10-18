package admin

import (
	"bd2server/internal/common/util"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
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
	// RoleOwner manages the whole server, including administrators. It
	// belongs to the game's administrator account and to sign-in links
	// printed by the server.
	RoleOwner Role = "owner"
	// RoleAdministrator holds only the permissions the owner granted.
	RoleAdministrator Role = "administrator"
)

// Store persists console sessions and administrators in state/admin.db.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

type session struct {
	AccountID   string
	Role        Role
	CSRF        string
	Expires     time.Time
	Permissions []string
}

// Administrator is an account the owner appointed, with its permissions.
type Administrator struct {
	AccountID   string
	Permissions []string
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
		`CREATE TABLE IF NOT EXISTS sessions (token_hash BLOB PRIMARY KEY NOT NULL, account_id TEXT NOT NULL, role TEXT NOT NULL CHECK(role IN ('owner','administrator')), csrf TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS administrators (account_id TEXT PRIMARY KEY NOT NULL CHECK(length(account_id)>0), permissions TEXT NOT NULL, appointed_at INTEGER NOT NULL, updated_at INTEGER NOT NULL) WITHOUT ROWID`,
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
	if token, err = util.RandomBase64URL(32); err != nil {
		return "", "", err
	}
	if csrf, err = util.RandomBase64URL(24); err != nil {
		return "", "", err
	}
	now := s.now()
	if _, err = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<=?`, now.Unix()); err != nil {
		return "", "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions VALUES(?,?,?,?,?,?)`, digest(token), accountID, string(role), csrf, now.Unix(), now.Add(sessionTTL).Unix())
	return token, csrf, err
}

// Session resolves a live session token with the administrator's current
// permissions, so revocation and permission changes apply immediately.
func (s *Store) Session(ctx context.Context, token string) (session, bool, error) {
	if token == "" {
		return session{}, false, nil
	}
	var result session
	var role string
	var expires int64
	var permissions sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT s.account_id,s.role,s.csrf,s.expires_at,a.permissions FROM sessions s
		LEFT JOIN administrators a ON a.account_id=s.account_id
		WHERE s.token_hash=? AND s.expires_at>? AND (s.role='owner' OR a.account_id IS NOT NULL)`,
		digest(token), s.now().Unix()).Scan(&result.AccountID, &role, &result.CSRF, &expires, &permissions)
	if errors.Is(err, sql.ErrNoRows) {
		return session{}, false, nil
	}
	if err != nil {
		return session{}, false, err
	}
	result.Role, result.Expires = Role(role), time.Unix(expires, 0)
	if result.Role == RoleAdministrator {
		if err := json.Unmarshal([]byte(permissions.String), &result.Permissions); err != nil {
			return session{}, false, fmt.Errorf("admin: decode permissions: %w", err)
		}
	}
	return result, true, nil
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, digest(token))
	return err
}

func (s *Store) Administrators(ctx context.Context) (map[string]Administrator, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT account_id,permissions FROM administrators`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]Administrator{}
	for rows.Next() {
		var administrator Administrator
		var permissions string
		if err := rows.Scan(&administrator.AccountID, &permissions); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(permissions), &administrator.Permissions); err != nil {
			return nil, fmt.Errorf("admin: decode permissions: %w", err)
		}
		out[administrator.AccountID] = administrator
	}
	return out, rows.Err()
}

func (s *Store) IsAdministrator(ctx context.Context, accountID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM administrators WHERE account_id=?)`, accountID).Scan(&exists)
	return exists, err
}

// SetAdministrator appoints or updates an administrator with the given,
// already normalized, permissions.
func (s *Store) SetAdministrator(ctx context.Context, accountID string, permissions []string) error {
	if accountID == "" {
		return errors.New("admin: administrator requires an account")
	}
	encoded, err := json.Marshal(append([]string{}, permissions...))
	if err != nil {
		return err
	}
	now := s.now().Unix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO administrators VALUES(?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET permissions=excluded.permissions,updated_at=excluded.updated_at`,
		accountID, string(encoded), now, now)
	return err
}

// RemoveAdministrator revokes an appointment and ends its sessions.
func (s *Store) RemoveAdministrator(ctx context.Context, accountID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM administrators WHERE account_id=?`, accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE account_id=? AND role='administrator'`, accountID); err != nil {
		return err
	}
	return tx.Commit()
}

func digest(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
