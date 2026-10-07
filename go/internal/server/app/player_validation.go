package app

import (
	"bd2server/internal/server/domain/command"
	"errors"
)

type accountStateInitializer interface {
	EnsurePersisted(command.Context) error
}

func ensureAccountStateInitialized(ctx command.Context, stores ...accountStateInitializer) error {
	for _, store := range stores {
		if store == nil {
			return errors.New("nil account state initializer")
		}
		if err := store.EnsurePersisted(ctx); err != nil {
			return err
		}
	}
	return nil
}
