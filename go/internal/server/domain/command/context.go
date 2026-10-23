// Package command identifies an authorized player command independently of transport and runtime.
package command

import (
	"bd2server/internal/common/util"
	"bd2server/internal/server/storage/stateio"
	"context"
	"errors"
)

type Identity struct{ AccountID, SessionID, RequestID string }
type Context struct {
	Identity
	Cancellation context.Context
	State        stateio.AtomicEntryStore
	Emit         func(Event) error
	InstanceIDs  util.InstanceIDSource
}

func (c Context) NewInstanceID() (uint64, error) {
	if c.InstanceIDs != nil {
		return c.InstanceIDs.NewInstanceID()
	}
	if source, ok := c.State.(util.InstanceIDSource); ok {
		return source.NewInstanceID()
	}
	return 0, errors.New("command: global instance ID source unavailable")
}

func (c Context) Cancelled() bool { return c.Cancellation != nil && c.Cancellation.Err() != nil }
