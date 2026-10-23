//go:build integration

package app

import (
	"context"
	"encoding/binary"
	"slices"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func TestFirstAvatarChoiceAndPartsBecomePersistentProfileEquipment(t *testing.T) {
	// AvatarPacket buys a character and its authored outfit before AvatarSave.
	// Current GameData: shop 1's nominal price is 1000 type44 and grants char1
	// with hair10001, outfit40001, head20001. The server's first-choice policy
	// waives that price once; later purchases retain the authored prices.
	factory, accounts := newIntegrationFactory(t)
	owner, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	g := &puzzleGameplay{t: t, factory: factory, account: accounts[0], owner: owner}
	t.Cleanup(func() {
		if g.owner != nil {
			if err := g.owner.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx := command.Context{State: tx, InstanceIDs: g.factory.instanceIDs()}
	if _, err = g.owner.assembly.eventEconomy.Apply(ctx, "avatar-test-budget", nil, []gamedata.Reward{{Type: 44, Count: 2500}}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	budget := g.wordCurrency()
	price := wire.AppendVarint(nil, 3, 44)
	price = wire.AppendVarint(price, 4, 1000)
	product := wire.AppendVarint(nil, 1, 1)
	product = wire.AppendBytes(product, 2, price)
	buy := g.request("/AvatarShopBuy", wire.AppendBytes(nil, 2, product))
	g.execute(buy, false)
	if got := g.wordCurrency(); got != budget {
		t.Fatalf("first choice changed balance=%d want %d", got, budget)
	}
	for _, asset := range [][2]uint64{{61, 1}, {49, 10001}, {49, 20001}, {49, 40001}} {
		if g.itemCount(asset[0], asset[1]) != 1 {
			t.Fatalf("purchased avatar asset %v is not actually owned", asset)
		}
	}
	g.execute(buy, false)
	if g.wordCurrency() != budget || g.itemCount(61, 1) != 1 {
		t.Fatal("replayed avatar purchase charged or granted again")
	}
	unowned := wire.AppendVarint(nil, 1, 1)
	unowned = wire.AppendVarint(unowned, 2, 10002)
	g.execute(g.request("/AvatarSave", wire.AppendBytes(nil, 2, unowned)), true)
	use := wire.AppendVarint(nil, 1, 1)
	use = wire.AppendVarint(use, 2, 10001)
	use = wire.AppendVarint(use, 3, 20001)
	use = wire.AppendVarint(use, 5, 40001)
	g.call("/AvatarSave", wire.AppendBytes(nil, 2, use))
	g.call("/AvatarMotionShopBuy", wire.AppendVarint(nil, 2, 100001))
	if g.itemCount(50, 100001) != 1 || g.wordCurrency() != budget {
		t.Fatal("free motion was not granted without currency consumption")
	}
	g.execute(g.request("/AvatarMotionShopBuy", wire.AppendVarint(nil, 2, 100001)), true)
	if g.itemCount(50, 100001) != 1 || g.wordCurrency() != budget {
		t.Fatal("repeated free motion changed ownership or charged currency")
	}
	wishlist := wire.AppendVarint(nil, 2, 40015)
	wishlist = wire.AppendVarint(wishlist, 2, 2)
	g.call("/AvatarShopWishListSave", wishlist)
	g.reopen()
	info := g.call("/AvatarInfo", nil)
	used, found, err := wire.Bytes(info, 1)
	if err != nil || !found {
		t.Fatalf("avatar equipment missing after reopening: %v", err)
	}
	for field, want := range map[int]uint64{1: 1, 2: 10001, 3: 20001, 5: 40001} {
		if got, _, err := wire.Varint(used, field); err != nil || got != want {
			t.Fatalf("saved avatar slot %d=%d want %d: %v", field, got, want, err)
		}
	}
	identity, err := factory.profiles.GameIdentity(context.Background(), accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	public := g.call("/UserContentsInfo", wire.AppendVarint(nil, 2, uint64(identity.OwnerIndex)))
	profileUse, found, err := wire.Bytes(public, 34)
	if err != nil || !found {
		t.Fatalf("profile does not project actual saved avatar: %v", err)
	}
	if got, _, _ := wire.Varint(profileUse, 5); got != 40001 {
		t.Fatalf("profile outfit=%d want purchased outfit40001", got)
	}
	login := g.call("/LoginUser", nil)
	user, _, err := wire.Bytes(login, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, _, err := wire.Varint(user, 56); err != nil || got != 1 {
		t.Fatalf("login forgot initial avatar selection: %d %v", got, err)
	}
	list := g.call("/AvatarShopWishListInfo", nil)
	decodeWishlist := func(data []byte) []uint64 {
		t.Helper()
		var ids []uint64
		if err := wire.Walk(data, func(f wire.Field) error {
			if f.Number != 1 {
				return nil
			}
			if f.Type != 0 && f.Type != 2 {
				return wire.ErrMalformed
			}
			remaining := f.Value
			for len(remaining) > 0 {
				value, size := binary.Uvarint(remaining)
				if size <= 0 {
					return wire.ErrMalformed
				}
				ids = append(ids, value)
				remaining = remaining[size:]
				if f.Type == 0 && len(remaining) != 0 {
					return wire.ErrMalformed
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	ids := decodeWishlist(list)
	if len(ids) != 2 || !slices.Contains(ids, 40015) || !slices.Contains(ids, 2) {
		t.Fatalf("wishlist did not survive reopening: %v", ids)
	}
	g.call("/AvatarShopWishListSave", nil)
	g.reopen()
	list = g.call("/AvatarShopWishListInfo", nil)
	if len(decodeWishlist(list)) != 0 {
		t.Fatal("wishlist removal did not clear the persisted list")
	}
	if g.wordCurrency() != budget || g.itemCount(61, 1) != 1 || g.itemCount(50, 100001) != 1 {
		t.Fatal("reopening or profile queries changed purchased assets")
	}
}

func TestAvatarDiamondPurchaseAndInvalidEquipmentBatchRemainAtomic(t *testing.T) {
	// AvatarPacket's purchase callback removes total diamonds, free first.
	// Current shop 2's premium price is 2000 diamonds. AvatarEditorUI also
	// excludes non-private parts for collab char18, including outfit40015.
	factory, accounts := newIntegrationFactory(t)
	var err error
	factory.seeds.login.UserInfo, _, err = wire.ReplaceVarint(factory.seeds.login.UserInfo, 8, 1250)
	if err != nil {
		t.Fatal(err)
	}
	factory.seeds.login.UserInfo, _, err = wire.ReplaceVarint(factory.seeds.login.UserInfo, 9, 1000)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	g := &puzzleGameplay{t: t, factory: factory, account: accounts[0], owner: owner}
	t.Cleanup(func() {
		if g.owner != nil {
			if err := g.owner.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	price := wire.AppendVarint(nil, 3, 3)
	price = wire.AppendVarint(price, 4, 2000)
	product := wire.AppendVarint(nil, 1, 2)
	product = wire.AppendBytes(product, 2, price)
	g.call("/AvatarShopBuy", wire.AppendBytes(nil, 2, product))
	g.reopen()
	readWallet := func() (uint64, uint64) {
		t.Helper()
		tx, err := g.owner.repository.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		wallet := g.owner.assembly.wallet.Snapshot(command.Context{State: tx})
		return wallet.FreeJewelry, wallet.Jewelry
	}
	if free, paid := readWallet(); free != 0 || paid != 250 {
		t.Fatalf("mixed diamond purchase persisted free=%d paid=%d want 0/250", free, paid)
	}
	if g.itemCount(61, 2) != 1 {
		t.Fatal("mixed diamond purchase did not grant the character")
	}
	use := wire.AppendVarint(nil, 1, 2)
	use = wire.AppendVarint(use, 2, 10002)
	use = wire.AppendVarint(use, 5, 40002)
	g.call("/AvatarSave", wire.AppendBytes(nil, 2, use))
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.owner.assembly.eventEconomy.Apply(command.Context{State: tx, InstanceIDs: g.factory.instanceIDs()}, "avatar-batch-and-collab-budget", nil, []gamedata.Reward{{Type: 44, Count: 2000}, {Type: 61, ID: 18, Count: 1}, {Type: 49, ID: 40015, Count: 1}}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	budget := g.wordCurrency()
	invalidUse := wire.AppendVarint(nil, 1, 18)
	invalidUse = wire.AppendVarint(invalidUse, 5, 40015)
	g.execute(g.request("/AvatarSave", wire.AppendBytes(nil, 2, invalidUse)), true)
	price = wire.AppendVarint(nil, 3, 44)
	price = wire.AppendVarint(price, 4, 1000)
	product = wire.AppendVarint(nil, 1, 3)
	product = wire.AppendBytes(product, 2, price)
	first := g.request("/AvatarShopBuy", wire.AppendBytes(nil, 2, product))
	last := g.request("/AvatarSave", wire.AppendBytes(nil, 2, invalidUse))
	first.Requests = append(first.Requests, last.Requests...)
	g.execute(first, true)
	g.reopen()
	if g.wordCurrency() != budget || g.itemCount(61, 3) != 0 || g.itemCount(49, 10003) != 0 {
		t.Fatal("failed equipment batch retained the preceding avatar purchase or currency debit")
	}
	info := g.call("/AvatarInfo", nil)
	current, found, err := wire.Bytes(info, 1)
	if err != nil || !found {
		t.Fatalf("committed equipment missing: %v", err)
	}
	if char, _, _ := wire.Varint(current, 1); char != 2 {
		t.Fatalf("rejected collaboration equipment replaced committed character: %d", char)
	}
	if outfit, _, _ := wire.Varint(current, 5); outfit != 40002 {
		t.Fatalf("rejected collaboration equipment replaced committed outfit: %d", outfit)
	}
	if free, paid := readWallet(); free != 0 || paid != 250 {
		t.Fatal("failed equipment batch changed the already committed diamond split")
	}
}
