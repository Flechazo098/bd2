package deck

import "bd2server/internal/server/domain/command"

import (
	"bd2server/internal/server/domain/roster"
)

func (s *Store) AttachAutoRecovery(f func(ctx command.Context, _ uint64, _ uint64, _ []uint64) (roster.AutoRecoveryResult, error)) {

	s.autoRecovery = f
}
func (s *Store) AttachAutoRecoveryAllowed(f func(ctx command.Context) (bool, error)) {

	s.autoRecoveryAllowed = f
}

type autoRecoveryReceipt struct {
	Digest string
	Body   []byte
}
