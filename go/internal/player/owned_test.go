package player

import (
	"path/filepath"
	"testing"

	"bd2server/internal/gamedata"
	"bd2server/internal/wire"
)

func TestBattleRewardPersistsWithoutDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned.json")
	starter := &Starter{Version: "2.34.13"}
	inv, err := OpenInventory(path, starter)
	if err != nil {
		t.Fatal(err)
	}
	reward := []gamedata.BattleReward{{Type: 8, ID: 8, Count: 3}}
	items, err := inv.GrantOnce("pack21:monster1", reward)
	if err != nil || len(items) != 1 || items[0].Count != 3 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	inv, err = OpenInventory(path, starter)
	if err != nil {
		t.Fatal(err)
	}
	items, err = inv.GrantOnce("pack21:monster1", reward)
	if err != nil || len(items) != 0 {
		t.Fatalf("duplicate grant=%+v err=%v", items, err)
	}
	code, response, ok, err := inv.Handle("/ItemInfo", wire.AppendVarint(nil, 1, 77))
	if err != nil || !ok || code != 21 {
		t.Fatalf("response=%d ok=%v err=%v", code, ok, err)
	}
	item, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatal("reward missing from inventory")
	}
	got, _, _ := wire.Varint(item, 2)
	if got != 8 {
		t.Fatalf("item id=%d", got)
	}
}
