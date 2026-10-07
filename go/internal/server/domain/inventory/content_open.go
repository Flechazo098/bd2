package inventory

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/storage/stateio"
)

const contentOpenPacketCode = 622

type contentOpenReceipt struct {
	Digest string `json:"digest"`
	Body   []byte `json:"body"`
}

type contentOpenState struct {
	Receipts map[string]contentOpenReceipt `json:"receipts"`
}

// ContentOpenService grants the completion ticket for the client's sole
// implemented ContentOpen type (1). The prerequisite ticket is never spent.
// Handle must run in the account request transaction: inventory and receipt
// writes commit together, and account recovery reconstructs Inventory after
// a dirty rollback. Receipts are reloaded for every request.
type ContentOpenService struct {
	design     *gamedata.ContentOpeningDesign
	inventory  *Inventory
	store      stateio.Store
	squadLevel func() (uint64, error)
}
