package events

import (
	"bd2server/internal/server/design/gamedata"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/storage/stateio"
	"fmt"
)

type BoxService struct {
	resources *gamedata.ResourceItemDesign
	items     *assets.Inventory
	economy   *Economy
	store     stateio.Store
}

func (s *BoxService) AttachResourceItems(design *gamedata.ResourceItemDesign) { s.resources = design }

type boxReceipt struct{ Request, Response []byte }

func OpenBoxes(store stateio.Store, items *assets.Inventory, economy *Economy) (*BoxService, error) {
	if store == nil || items == nil || economy == nil {
		return nil, fmt.Errorf("events: invalid box runtime")
	}
	return &BoxService{store: store, items: items, economy: economy}, nil
}
