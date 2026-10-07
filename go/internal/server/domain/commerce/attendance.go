package commerce

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
)

type attendanceHandler interface {
	Handle(ctx command.Context, _ string, _ []byte) (int, []byte, bool, error)
}

// AttendanceHandler preserves the original event progress response and adds
// subscription grants in extension fields understood by the commerce plugin.
// All operations execute inside the transport's account transaction.
type AttendanceHandler struct {
	Events      attendanceHandler
	Economy     *EntitlementEconomy
	LoginPasses *LoginPasses
	Store       stateio.Store
}

type attendanceReceipt struct {
	Digest string `json:"digest"`
	Bundle []byte `json:"bundle"`
}
