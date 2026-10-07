// Package command identifies an authorized player command independently of transport and runtime.
package command

import (
	"bd2server/internal/server/storage/stateio"
	"context"
)

type Identity struct{ AccountID, SessionID, RequestID string }
type Context struct {
	Identity
	Cancellation context.Context
	State        stateio.AtomicEntryStore
}

func (c Context) Cancelled() bool { return c.Cancellation != nil && c.Cancellation.Err() != nil }
