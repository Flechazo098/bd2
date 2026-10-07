package roster

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/storage/stateio"
	"fmt"
)

type itemCraftReceipt struct {
	Digest string
	Body   []byte
}
type ItemCraftService struct {
	design     *gamedata.ItemCraftDesign
	talents    *gamedata.TalentUseDesign
	store      stateio.Store
	items      *assets.Inventory
	characters *CharacterStore
	wallet     *assets.Wallet
	known      func(ctx command.Context, _ uint64) bool
	context    func(command.Context) (int, bool, error)
}

func NewItemCraftService(d *gamedata.ItemCraftDesign, talents *gamedata.TalentUseDesign, store stateio.Store, items *assets.Inventory, characters *CharacterStore, wallet *assets.Wallet, known func(ctx command.Context, _ uint64) bool) (*ItemCraftService, error) {
	if d == nil || talents == nil || store == nil || items == nil || characters == nil || wallet == nil || known == nil {
		return nil, fmt.Errorf("craft: missing dependencies")
	}
	return &ItemCraftService{design: d, talents: talents, store: store, items: items, characters: characters, wallet: wallet, known: known}, nil
}
func (s *ItemCraftService) AttachContext(ctx command.Context, context func(command.Context) (int, bool, error)) {
	s.context = context
}
