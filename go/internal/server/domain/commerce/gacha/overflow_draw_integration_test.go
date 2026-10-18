//go:build integration

package gacha

import (
	"bytes"
	"testing"

	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

func TestTwelvePickMaxedDrawHasPlayableResultsAndCreditsOnce(t *testing.T) {
	v, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := gamedata.CloseDatabaseCache(); err != nil {
			t.Error(err)
		}
	})
	catalog, err := gamedata.LoadRegularCostumeGachaGroups(v.Resolve("data/resources/GameData"), v.GameDataVersion, []uint64{10001}, nil)
	if err != nil {
		t.Fatal(err)
	}
	group, ok := catalog.Group(10001)
	if !ok {
		t.Fatal("12pick unavailable")
	}
	var base []roster.Costume
	seen := map[uint64]bool{}
	for _, draw := range catalog.Gachas {
		for id := range draw.Grades {
			if seen[id] {
				continue
			}
			seen[id] = true
			design, ok := catalog.Character(id)
			if !ok {
				t.Fatal("missing costume design")
			}
			base = append(base, roster.Costume{InvenIndex: uint64(len(base) + 1), ID: id, Level: design.CostumeMaxLevel})
		}
	}
	state := stateio.NewMemory()
	ctx := command.Context{Identity: command.Identity{AccountID: "overflow-player", SessionID: "draw-session"}, State: state}
	var service *Service
	open := func() {
		t.Helper()
		collection, err := roster.OpenCollectionStore(ctx, stateio.RootStore{}, base)
		if err != nil {
			t.Fatal(err)
		}
		wallet, err := assets.OpenWallet(ctx, stateio.RootStore{}, assets.Currency{FreeJewelry: 10000})
		if err != nil {
			t.Fatal(err)
		}
		service, err = NewService(nil, catalog, collection, wallet)
		if err != nil {
			t.Fatal(err)
		}
	}
	open()
	ids := []uint64{806, 101, 102, 103, 104, 105, 106, 201, 202, 203, 204, 301}
	var choices []roster.GachaSelection
	for i, id := range ids {
		choices = append(choices, roster.GachaSelection{GroupID: group.ID, Slot: uint64(i), ItemID: id})
	}
	if _, _, _, err := service.Handle(ctx, "/GachaSelectionSave", selectionRequest(choices)); err != nil {
		t.Fatal(err)
	}
	for i, id := range []uint64{group.OneTimeGachaID, group.TenTimeGachaID} {
		before := service.wallet.Snapshot(ctx)
		request := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, uint64(i+10)), 2, id), 3, 1)
		_, body, handled, err := service.Handle(ctx, "/GachaBuy", request)
		if err != nil || !handled {
			t.Fatalf("draw: %v", err)
		}
		bundle, _, err := wire.Bytes(body, 1)
		if err != nil {
			t.Fatal(err)
		}
		items := map[uint64][]byte{}
		exchanges := map[uint64][]byte{}
		err = wire.Walk(bundle, func(f wire.Field) error {
			switch f.Number {
			case 1:
				sort, _, _ := wire.Varint(f.Value, 9)
				items[sort] = f.Value
			case 8:
				sort, _, _ := wire.Varint(f.Value, 7)
				exchanges[sort] = f.Value
			case 2, 3, 9, 10:
				t.Fatal("maxed draw changed ownership/enhancement or sent duplicate currency credit")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		draw, _ := catalog.Gacha(id)
		if len(items) != draw.Count || len(exchanges) != draw.Count {
			t.Fatal("native result animation cannot restore every draw")
		}
		var earned uint64
		for slot := 0; slot < draw.Count; slot++ {
			item, exchange := items[uint64(slot)], exchanges[uint64(slot)]
			costume, _, _ := wire.Varint(exchange, 2)
			design, ok := catalog.Character(costume)
			if !ok {
				t.Fatal("result is not a costume from this banner")
			}
			itemType, _, _ := wire.Varint(item, 3)
			itemID, _, _ := wire.Varint(item, 2)
			count, _, _ := wire.Varint(item, 4)
			originalType, _, _ := wire.Varint(exchange, 1)
			originalCount, _, _ := wire.Varint(exchange, 3)
			targetType, _, _ := wire.Varint(exchange, 4)
			targetID, _, _ := wire.Varint(exchange, 5)
			targetCount, _, _ := wire.Varint(exchange, 6)
			if originalType != 11 || originalCount != 1 || targetType != itemType || targetID != itemID || targetCount != count {
				t.Fatal("native exchange cannot restore one costume from its compensation")
			}
			if itemType != design.OverflowItemType || itemID != design.OverflowItemID || count != design.OverflowItemCount {
				t.Fatal("native exchange cannot match the actual compensation by type/id/sort")
			}
			earned += count
		}
		after := service.wallet.Snapshot(ctx)
		if after.Mileage != before.Mileage+earned || after.FreeJewelry != before.FreeJewelry-draw.Price {
			t.Fatal("draw cost or compensation differs from owned assets")
		}
		open()
		_, retry, _, err := service.Handle(ctx, "/GachaBuy", request)
		if err != nil || !bytes.Equal(body, retry) || service.wallet.Snapshot(ctx) != after {
			t.Fatal("reopened draw retry changed results or assets")
		}
	}
}
