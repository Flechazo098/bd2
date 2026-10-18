package events

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	accountstate "bd2server/internal/server/storage/account"
	"bd2server/internal/server/storage/stateio"
)

type resourceCostumes struct{}

func (resourceCostumes) Character(id uint64) (gamedata.CharacterDesign, bool) {
	return gamedata.CharacterDesign{ID: 77, HP: 100, CostumeMaxLevel: 1, OverflowItemType: 20, OverflowItemCount: 10}, id == 701
}

type resourceGraph struct{}

func (resourceGraph) Resolve(r []gamedata.BattleReward) ([]gamedata.BattleReward, error) {
	return r, nil
}

type resourceOptions struct{}

func (resourceOptions) RollOptions(uint64) ([]gamedata.EquipmentOptionChoice, []gamedata.EquipmentOptionChoice, *gamedata.EquipmentOptionChoice, error) {
	return nil, nil, nil, nil
}

type resourceRuntime struct {
	ctx        command.Context
	box        *BoxService
	items      *assets.Inventory
	wallet     *assets.Wallet
	collection *roster.CollectionStore
}

func openResourceRuntime(t *testing.T, state stateio.AtomicEntryStore, initial bool) resourceRuntime {
	t.Helper()
	ctx := command.Context{Identity: command.Identity{AccountID: "selector", SessionID: "login"}, State: state}
	store := stateio.RootStore{}
	items, err := assets.OpenInventory(ctx, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if initial {
		if err := items.InitializeItems(ctx, []assets.Item{{InvenIndex: 900000010, Type: 8, ID: 501, Count: 4}, {InvenIndex: 900000011, Type: 8, ID: 502, Count: 1}, {InvenIndex: 900000012, Type: 9, ID: 501, Count: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	wallet, err := assets.OpenWallet(ctx, store, assets.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := roster.OpenCollectionStore(ctx, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	equipment, err := assets.OpenEquipmentInventory(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	gameplay := stateio.EntrySnapshotStore{Domain: "collection", Bucket: "gameplay"}
	economy, err := NewEconomy(ctx, gameplay, items, wallet, collection, equipment, resourceCostumes{}, resourceOptions{}, resourceGraph{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	box, err := OpenBoxes(gameplay, items, economy)
	if err != nil {
		t.Fatal(err)
	}
	box.AttachResourceItems(&gamedata.ResourceItemDesign{Selections: map[uint64]map[uint64]gamedata.BattleReward{501: {701: {Type: 11, ID: 701, Count: 1}}}})
	return resourceRuntime{ctx, box, items, wallet, collection}
}

func resourceRequest(seq, index, count, selection uint64) []byte {
	b := wire.AppendVarint(nil, 1, seq)
	b = wire.AppendVarint(b, 2, index)
	b = wire.AppendVarint(b, 3, count)
	return wire.AppendVarint(b, 4, selection)
}

func (r resourceRuntime) use(t *testing.T, req []byte) []byte {
	t.Helper()
	code, out, handled, err := r.box.Handle(r.ctx, "/UseResourceItem", req)
	if err != nil || !handled || code != 271 {
		t.Fatalf("use resource failed: %d %v", code, err)
	}
	return out
}

func TestResourceChoiceConsumesInventoryAndRestoresCostumeUpgradeAndOverflow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	tx, err := repo.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := openResourceRuntime(t, tx, true)
	req := resourceRequest(1, 900000010, 3, 701)
	first := r.use(t, req)
	if len(r.collection.Costumes()) != 1 || len(r.collection.Characters()) != 1 || r.collection.Costumes()[0].ID != 701 || r.collection.Costumes()[0].Level != 1 || r.wallet.Snapshot(r.ctx).Mileage != 10 {
		t.Fatal("choice did not create, upgrade then exchange the selected costume")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err = repo.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	r = openResourceRuntime(t, tx, false)
	if !bytes.Equal(first, r.use(t, req)) {
		t.Fatal("retry after database reopen changed resource response")
	}
	if r.wallet.Snapshot(r.ctx).Mileage != 10 || len(r.collection.Costumes()) != 1 || r.collection.Costumes()[0].Level != 1 {
		t.Fatal("reopened retry duplicated selected rewards")
	}
	remaining := uint64(0)
	for _, item := range r.items.All(r.ctx) {
		if item.InvenIndex == 900000010 {
			remaining = item.Count
		}
	}
	if remaining != 1 {
		t.Fatalf("ticket consumption lost or duplicated after reopen: %d", remaining)
	}
	r.use(t, resourceRequest(2, 900000010, 1, 701))
	if r.wallet.Snapshot(r.ctx).Mileage != 20 {
		t.Fatal("new request did not consume final ticket and exchange the maxed costume")
	}
}

func TestResourceChoiceRejectsInvalidSelectionsAndCannotUseAnotherAccountInventory(t *testing.T) {
	state := stateio.NewMemory()
	r := openResourceRuntime(t, state, true)
	for _, req := range [][]byte{
		resourceRequest(1, 900000010, 0, 701), resourceRequest(1, 900000010, 5, 701),
		resourceRequest(1, 900000010, 1, 702), resourceRequest(1, 900000011, 1, 701),
		resourceRequest(1, 900000012, 1, 701), resourceRequest(1, 123, 1, 701),
		wire.AppendVarint(resourceRequest(1, 900000010, 1, 701), 4, 701),
	} {
		if _, _, _, err := r.box.Handle(r.ctx, "/UseResourceItem", req); err == nil {
			t.Fatal("invalid resource use succeeded")
		}
	}
	if len(r.collection.Costumes()) != 0 || r.items.All(r.ctx)[0].Count != 4 || r.wallet.Snapshot(r.ctx).Mileage != 0 {
		t.Fatal("rejected requests changed owned assets")
	}
	r.use(t, resourceRequest(9, 900000010, 1, 701))
	if _, _, _, err := r.box.Handle(r.ctx, "/UseResourceItem", resourceRequest(9, 900000010, 2, 701)); err == nil {
		t.Fatal("sequence conflict consumed more tickets")
	}
	other := openResourceRuntime(t, stateio.NewMemory(), false)
	other.ctx.AccountID = "other"
	if _, _, _, err := other.box.Handle(other.ctx, "/UseResourceItem", resourceRequest(1, 900000010, 1, 701)); err == nil {
		t.Fatal("another account used the selector's ticket")
	}
	if len(other.collection.Costumes()) != 0 || len(other.items.All(other.ctx)) != 0 {
		t.Fatal("rejected cross-account use created assets")
	}
}
