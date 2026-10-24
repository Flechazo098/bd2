package app

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

// ItemStackRefreshNotifyField is a Notify field the native client ignores. The
// LocalIdentity plugin reloads the inventories its value names.
const ItemStackRefreshNotifyField = 1000

// Bits of ItemStackRefreshNotifyField.
const (
	itemStackRefreshItems = 1 << 0 // ordinary inventory and item storage
	itemStackRefreshLife  = 1 << 1 // life content items
)

// itemStackObserver applies the account's item stack switch while it plays:
// before each request it reshapes the stacks to the current switch, and when
// that changed anything the reply asks the plugin to reload the inventory.
type itemStackObserver struct {
	player  *playerAssembly
	changed uint64
}

func (o *itemStackObserver) BeforeDispatch(ctx command.Context, path string, _ []byte) error {
	changed, err := o.player.normalizeItemStacks(ctx)
	// The login reply is followed by the client's own full inventory requests.
	if path != "/LoginUser" {
		o.changed |= changed
	}
	return err
}

func (o *itemStackObserver) AfterDispatch(command.Context, string, []byte, []byte) ([]byte, error) {
	if o.changed == 0 {
		return nil, nil
	}
	changed := o.changed
	o.changed = 0
	return wire.AppendVarint(nil, ItemStackRefreshNotifyField, changed), nil
}

// normalizeItemStacks reshapes inventory and life item stacks to the account's
// switch and reports which of them changed as itemStackRefresh bits.
func (p *playerAssembly) normalizeItemStacks(ctx command.Context) (uint64, error) {
	var changed uint64
	items, err := p.ownedItems.NormalizeItemStacks(ctx)
	if err != nil {
		return 0, err
	}
	if items {
		changed |= itemStackRefreshItems
	}
	life, err := p.lifeService.TrimItemStacks(ctx)
	if life {
		changed |= itemStackRefreshLife
	}
	return changed, err
}
