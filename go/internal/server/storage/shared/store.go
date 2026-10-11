package shared

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"

	accountstate "bd2server/internal/server/storage/account"
	"bd2server/internal/server/storage/outbox"
)

type Store struct {
	repository *accountstate.Repository
	capacity   chan struct{}
}

var ErrBusy = errors.New("shared transaction capacity exhausted")

type Tx struct {
	command  *accountstate.CommandStore
	writable bool
}

type Entry struct {
	Key   string
	Value []byte
}

func Open(directory string) (*Store, error) {
	if directory == "" {
		return nil, errors.New("shared state directory is empty")
	}
	r, err := accountstate.Open(filepath.Join(directory, "state.db"))
	if err != nil {
		return nil, err
	}
	return &Store{repository: r, capacity: make(chan struct{}, 128)}, nil
}

func (s *Store) Close() error { return s.repository.Close() }

func (s *Store) Update(ctx context.Context, apply func(*Tx) error) error {
	return s.transact(ctx, true, apply)
}

func (s *Store) View(ctx context.Context, read func(*Tx) error) error {
	return s.transact(ctx, false, read)
}

func (s *Store) transact(ctx context.Context, writable bool, apply func(*Tx) error) (result error) {
	if apply == nil {
		return errors.New("shared state callback is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.capacity <- struct{}{}:
		defer func() { <-s.capacity }()
	default:
		return ErrBusy
	}
	tx, err := s.repository.BeginCommand(ctx)
	if err != nil {
		return err
	}
	finished := false
	defer func() {
		if !finished {
			result = errors.Join(result, tx.Rollback())
		}
	}()
	if err := apply(&Tx{command: tx, writable: writable}); err != nil {
		return err
	}
	if !writable {
		finished = true
		return tx.Rollback()
	}
	finished = true
	return tx.Commit()
}

func (t *Tx) Get(bucket, key string) ([]byte, bool, error) {
	return t.command.LoadEntry("shared", bucket, key)
}

func (t *Tx) Put(bucket, key string, value []byte) error {
	if !t.writable {
		return errors.New("shared transaction is read-only")
	}
	return t.command.PutEntry("shared", bucket, key, value)
}

func (t *Tx) Delete(bucket, key string) (bool, error) {
	if !t.writable {
		return false, errors.New("shared transaction is read-only")
	}
	return t.command.DeleteEntry("shared", bucket, key)
}

func (t *Tx) Scan(bucket, prefix, after string, limit int) ([]Entry, error) {
	if limit < 1 || limit > 256 {
		return nil, errors.New("shared page limit must be between 1 and 256")
	}
	if after != "" && !strings.HasPrefix(after, prefix) {
		return nil, errors.New("shared cursor is outside requested prefix")
	}
	entries, err := t.command.EntryPrefixPage("shared", bucket, prefix, after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, Entry{Key: entry.Key, Value: entry.Payload})
	}
	return out, nil
}

func (t *Tx) Outbox() (outbox.PageStore, error) {
	if !t.writable {
		return nil, errors.New("shared transaction is read-only")
	}
	return t.command, nil
}

// Consume commits the target change and its inbox receipt together. Delivery
// acknowledgement belongs to the source's subsequent transaction.
func (s *Store) Consume(ctx context.Context, event outbox.Pending, apply func(*Tx, outbox.Envelope) error) error {
	if apply == nil {
		return errors.New("invalid shared inbox event")
	}
	key, digest, err := outbox.Validate(event)
	if err != nil {
		return err
	}
	return s.Update(ctx, func(tx *Tx) error {
		prior, found, err := tx.Get("inbox", key)
		if err != nil {
			return err
		}
		if found {
			if !bytes.Equal(prior, digest) {
				return outbox.ErrConflict
			}
			return nil
		}
		if err := apply(tx, event.Envelope); err != nil {
			return err
		}
		return tx.Put("inbox", key, digest)
	})
}
