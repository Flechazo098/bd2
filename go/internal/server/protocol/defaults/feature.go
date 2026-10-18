// Package feature handles the three intentionally stateless protocol endpoints.
package feature

import (
	"bd2server/internal/server/domain/command"
	"errors"
	"fmt"
	"maps"

	"bd2server/internal/server/protocol/wire"
)

// ErrInvalidRequest means a known endpoint was sent a malformed request.
var ErrInvalidRequest = errors.New("feature: invalid protobuf request")

var emptyResponses = map[string]int{
	"/PersonalInfo":        199,
	"/BalanceVersionCheck": 186,
	"/UpdateAgeGate":       0,
}

// Handle recognizes only audited, stateless empty-response endpoints. The
// request must be a structurally valid protobuf with its normal nonzero
// request sequence in field 1. Unknown paths return ok=false and no response.
// proto is nil on success: protobuf's canonical empty-message encoding is zero
// bytes.
func Handle(ctx command.Context, path string, request []byte) (packetCode int, proto []byte, ok bool, err error) {
	packetCode, ok = emptyResponses[path]
	if !ok {
		return 0, nil, false, nil
	}
	sequence, present, walkErr := wire.Varint(request, 1)
	if walkErr != nil || !present || sequence == 0 {
		if walkErr != nil {
			return 0, nil, true, fmt.Errorf("%w: %s: %v", ErrInvalidRequest, path, walkErr)
		}
		return 0, nil, true, fmt.Errorf("%w: %s has no request sequence", ErrInvalidRequest, path)
	}
	return packetCode, proto, true, nil
}

// Service adapts this registry to session.Handler without importing session.
type Service struct{}

func (Service) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	return Handle(ctx, path, request)
}

// EmptyPacketCodes returns a detached copy for diagnostics and protocol tests.
func EmptyPacketCodes() map[string]int {
	result := make(map[string]int, len(emptyResponses))
	maps.Copy(result, emptyResponses)
	return result
}
