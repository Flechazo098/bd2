package identitystore

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Store) Accounts(ctx context.Context, after string, limit int) ([]string, error) {
	if limit < 1 || limit > 256 {
		return nil, errors.New("account directory limit must be between 1 and 256")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT account_id FROM game_profiles WHERE account_id>? ORDER BY account_id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) AccountForOwner(ctx context.Context, owner uint64) (string, error) {
	if owner == 0 || owner > 1<<63-1 {
		return "", errors.New("invalid public owner index")
	}
	var account string
	err := s.db.QueryRowContext(ctx, `SELECT account_id FROM game_profiles WHERE owner_index=?`, owner).Scan(&account)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("public owner not found")
	}
	return account, err
}
