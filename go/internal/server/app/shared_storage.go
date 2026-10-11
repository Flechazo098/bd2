package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"bd2server/internal/server/domain/social"
	"bd2server/internal/server/storage/shared"
)

type socialStore struct{ store *shared.Store }
type socialTx struct{ *shared.Tx }

func (s socialStore) Update(ctx context.Context, apply func(social.Tx) error) error {
	return s.store.Update(ctx, func(tx *shared.Tx) error { return apply(socialTx{tx}) })
}
func (s socialStore) View(ctx context.Context, apply func(social.Tx) error) error {
	return s.store.View(ctx, func(tx *shared.Tx) error { return apply(socialTx{tx}) })
}
func (t socialTx) Scan(bucket, prefix, after string, limit int) ([]social.Entry, error) {
	entries, err := t.Tx.Scan(bucket, prefix, after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]social.Entry, len(entries))
	for i, e := range entries {
		out[i] = social.Entry{Key: e.Key, Value: e.Value}
	}
	return out, nil
}

type outboxDiscovery struct {
	factory  *PlayerFactory
	accounts interface {
		Accounts(context.Context, string, int) ([]string, error)
	}
}

func (d outboxDiscovery) Accounts(ctx context.Context, after string, limit int) ([]string, error) {
	if d.accounts != nil {
		return d.accounts.Accounts(ctx, after, limit)
	}
	if after < "local-owner" {
		return []string{"local-owner"}, nil
	}
	return nil, nil
}

func (d outboxDiscovery) HasPending(ctx context.Context, account string) (pending bool, result error) {
	path := filepath.Join(d.factory.options.stateDirectory, "accounts", accountDirectoryName(account), "state.db")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	uriPath := filepath.ToSlash(absolute)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(1000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return false, err
	}
	defer func() { result = errors.Join(result, db.Close()) }()
	db.SetMaxOpenConns(1)
	err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM domain_entry WHERE domain_name='outbox' AND bucket='pending' LIMIT 1)`).Scan(&pending)
	if err != nil {
		return false, fmt.Errorf("discover pending outbox for account %q: %w", account, err)
	}
	return pending, err
}
