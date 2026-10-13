package identitystore

import (
	"context"
	"errors"
	"time"
)

func (s *Store) RecordGameLogin(ctx context.Context, accountID string, at time.Time) error {
	if accountID == "" || at.Unix() <= 0 {
		return errors.New("auth: invalid game login record")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE accounts SET last_login_at=MAX(last_login_at,?) WHERE id=? AND status='active'`, at.Unix(), accountID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return errors.New("auth: game login account unavailable")
	}
	return nil
}
